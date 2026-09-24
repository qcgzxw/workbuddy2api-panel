package panel

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// importPanel 构造一个可测 cockpit 导入的面板：真实临时凭证目录 + mock 上游。
// 导入路径末尾会顺带签到/激活/查余额（幂等、失败仅记日志），mock 让它们不发网络。
//
// 凭证目录特意嵌成 <tmp>/a/auths：带 "../" 的 uid 经 filepath.Join 归一化后落点是
// <tmp>/a/evil.json（三级）或 <tmp>/evil.json（四级）—— 都还在 t.TempDir() 之内，
// 既能被「祖先目录不得出现多余条目」的断言抓住，又不会污染真实 /tmp。
func importPanel(t *testing.T) (*Panel, *pool.Pool, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "a", "auths")
	p := pool.New("")
	up := &upstream.Client{
		HTTP: &http.Client{Transport: &mockHTTPTransport{roundTrip: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{}}`)),
			}, nil
		}}},
		ChatBaseCN:     "https://fake.example",
		BillingBaseCN:  "https://fake.example",
		ChatBaseGlobal: "https://fake.example",
	}
	return New(Config{Pool: p, Upstream: up, AuthDir: dir, APIKey: "testkey"}), p, dir
}

// postImport 以 multipart 形式 POST 一段原始 JSON 内容到导入端点。
func postImport(t *testing.T, pn *Panel, raw string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "cockpit.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/panel/api/import/cockpit", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer testkey")
	rec := httptest.NewRecorder()
	pn.ServeHTTP(rec, req)
	return rec
}

// TestImportCockpitHappyPath 批量导入落盘 + 入池 + realm 按 domain 推断。
func TestImportCockpitHappyPath(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	pn, p, dir := importPanel(t)
	// expires_at 为毫秒时间戳（cockpit tools 口径）。
	raw := `[
	  {"id":"a1","email":"a1@x.com","uid":"uid-cn-1","access_token":"at1","refresh_token":"rt1",
	   "expires_at":4102444800000,"domain":"www.codebuddy.cn","nickname":"一号"},
	  {"id":"a2","email":"a2@x.com","uid":"uid-gl-1","access_token":"at2","refresh_token":"rt2",
	   "expires_at":4102444800000,"domain":"workbuddy.ai"}
	]`
	rec := postImport(t, pn, raw)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["imported"].(float64) != 2 || resp["skipped"].(float64) != 0 {
		t.Fatalf("imported/skipped 应为 2/0，got %v/%v（errors=%v）", resp["imported"], resp["skipped"], resp["errors"])
	}

	// 落盘：两个凭证文件都在，且过期时间从毫秒转成了秒。
	for _, uid := range []string{"uid-cn-1", "uid-gl-1"} {
		fp := filepath.Join(dir, "workbuddy-"+uid+".json")
		if _, err := os.Stat(fp); err != nil {
			t.Fatalf("%s 未落盘: %v", fp, err)
		}
	}
	got, err := auth.ParseFile(filepath.Join(dir, "workbuddy-uid-cn-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt != 4102444800 {
		t.Errorf("expires_at 应由毫秒转为秒，got %d", got.ExpiresAt)
	}
	if got.Nickname != "一号" {
		t.Errorf("nickname 未保留，got %q", got.Nickname)
	}

	// 入池。
	if p.AuthByUID("uid-cn-1") == nil || p.AuthByUID("uid-gl-1") == nil {
		t.Fatal("导入的账号应已入池")
	}
	// realm 落盘标识按 domain 推断（global 域写 global，CN 写 cn）。
	gl, err := auth.ParseFile(filepath.Join(dir, "workbuddy-uid-gl-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := gl.RealmStored(); got != "global" {
		t.Errorf("workbuddy.ai 域应落 realm=global，got %q", got)
	}
	cn, err := auth.ParseFile(filepath.Join(dir, "workbuddy-uid-cn-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cn.RealmStored(); got != "cn" {
		t.Errorf("codebuddy.cn 域应落 realm=cn，got %q", got)
	}
	// nickname 缺失时回落 email，不落空。
	if gl.Nickname != "a2@x.com" {
		t.Errorf("nickname 缺省应回落 email，got %q", gl.Nickname)
	}
}

// TestImportCockpitSkipsBadRows 单条脏数据只跳过该条，不中断整批。
func TestImportCockpitSkipsBadRows(t *testing.T) {
	pn, p, dir := importPanel(t)
	raw := `[
	  {"id":"ok","uid":"uid-ok","access_token":"at","refresh_token":"rt","domain":"www.codebuddy.cn"},
	  {"id":"nofields"},
	  {"id":"traversal3","uid":"../../../evil","access_token":"at","refresh_token":"rt"},
	  {"id":"traversal4","uid":"../../../../evil","access_token":"at","refresh_token":"rt"},
	  {"id":"slash","uid":"a/b","access_token":"at","refresh_token":"rt"}
	]`
	rec := postImport(t, pn, raw)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["imported"].(float64) != 1 {
		t.Fatalf("imported 应为 1，got %v（errors=%v）", resp["imported"], resp["errors"])
	}
	if resp["skipped"].(float64) != 4 {
		t.Fatalf("skipped 应为 4，got %v（errors=%v）", resp["skipped"], resp["errors"])
	}
	if errs := resp["errors"].([]any); len(errs) != 4 {
		t.Fatalf("应逐条记录跳过原因，got %v", errs)
	}
	if p.AuthByUID("uid-ok") == nil {
		t.Fatal("合法条目应入池")
	}

	// 路径穿越必须被挡住：凭证目录里只该有那一个合法文件。
	if names := dirNames(t, dir); len(names) != 1 || names[0] != "workbuddy-uid-ok.json" {
		t.Fatalf("凭证目录只应有合法条目，got %v", names)
	}
	// 祖先目录同样不得出现多余条目：uid "../../../evil" 归一化后的落点是
	// <tmp>/a/evil.json、uid "../../../../evil" 落 <tmp>/evil.json（见 importPanel 注释）。
	// 这两条断言才是真正能证伪「validUID 被摘掉」的检查。
	if names := dirNames(t, filepath.Dir(dir)); len(names) != 1 || names[0] != "auths" {
		t.Fatalf("路径穿越未被拦截：%s 出现多余条目 %v", filepath.Dir(dir), names)
	}
	if names := dirNames(t, filepath.Dir(filepath.Dir(dir))); len(names) != 1 || names[0] != "a" {
		t.Fatalf("路径穿越未被拦截：临时根出现多余条目 %v", names)
	}
}

// dirNames 列出目录条目名（排序稳定，便于断言）。
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// TestImportCockpitBadPayload 非数组 / 空数组 / 非法 JSON 一律 400，且不产生副作用。
func TestImportCockpitBadPayload(t *testing.T) {
	pn, _, dir := importPanel(t)
	for _, tc := range []struct {
		name, raw string
	}{
		{"invalid json", `{not json`},
		{"object not array", `{"uid":"u1"}`},
		{"empty array", `[]`},
	} {
		rec := postImport(t, pn, tc.raw)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code=%d want 400（body=%s）", tc.name, rec.Code, rec.Body.String())
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return // 目录都没建 —— 更彻底的无副作用
		}
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Errorf("失败请求不该留下任何凭证文件，got %d 个", len(ents))
	}
}

// TestImportCockpitRequiresAuth 导入端点受面板鉴权保护（与其余 /panel/api/* 一致）。
func TestImportCockpitRequiresAuth(t *testing.T) {
	pn, _, _ := importPanel(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "c.json")
	fw.Write([]byte(`[]`))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/panel/api/import/cockpit", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	pn.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无密钥应 401，got %d", rec.Code)
	}
}
