package panel

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/notify"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
	"github.com/linguo2625469/workbuddy2api-panel/internal/voucher"
)

type mockHTTPTransport struct {
	roundTrip func(req *http.Request) (*http.Response, error)
}

func (m *mockHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTrip(req)
}

func TestAccountRemarkAndVoucherStatusAPI(t *testing.T) {
	tmpDir := t.TempDir()
	authFile := filepath.Join(tmpDir, "workbuddy-u1.json")
	authJSON := `{"account":{"uid":"u1","nickname":"13800000000"},"auth":{"accessToken":"at","refreshToken":"rt","domain":"www.codebuddy.cn","realm":"cn"}}`
	_ = os.WriteFile(authFile, []byte(authJSON), 0o600)
	a, err := auth.ParseFile(authFile)
	if err != nil {
		t.Fatal(err)
	}

	p := pool.New("")
	p.Add(a)

	vStore, err := voucher.NewStore(filepath.Join(tmpDir, "vouchers.json"))
	if err != nil {
		t.Fatal(err)
	}

	pn := New(Config{
		Pool:         p,
		VoucherStore: vStore,
		APIKey:       "testkey",
	})

	// 1. 测试设置账号备注 POST /panel/api/accounts/u1/remark
	remarkReq := map[string]string{"remark": "测试备注1"}
	bodyBytes, _ := json.Marshal(remarkReq)
	req := httptest.NewRequest(http.MethodPost, "/panel/api/accounts/u1/remark", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer testkey")
	w := httptest.NewRecorder()
	pn.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if a.RemarkValue() != "测试备注1" {
		t.Errorf("expected remark '测试备注1', got %q", a.RemarkValue())
	}

	// 2. 测试标记券码状态 POST /panel/api/school/vouchers/status
	statusReq := map[string]any{
		"code":       "VC-12345",
		"is_used":    true,
		"uid":        "u1",
		"nickname":   a.DisplayName(),
		"prize_name": "肯德基冰淇淋",
		"valid_to":   "2026-10-24",
	}
	bodyBytes2, _ := json.Marshal(statusReq)
	req2 := httptest.NewRequest(http.MethodPost, "/panel/api/school/vouchers/status", bytes.NewReader(bodyBytes2))
	req2.Header.Set("Authorization", "Bearer testkey")
	w2 := httptest.NewRecorder()
	pn.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	if !vStore.IsUsed("VC-12345") {
		t.Errorf("expected VC-12345 to be marked as used in store")
	}

	// 3. 撤销标记已使用
	statusReq["is_used"] = false
	bodyBytes3, _ := json.Marshal(statusReq)
	req3 := httptest.NewRequest(http.MethodPost, "/panel/api/school/vouchers/status", bytes.NewReader(bodyBytes3))
	req3.Header.Set("Authorization", "Bearer testkey")
	w3 := httptest.NewRecorder()
	pn.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w3.Code, w3.Body.String())
	}
	if vStore.IsUsed("VC-12345") {
		t.Errorf("expected VC-12345 to be marked as unused")
	}
}

func TestAccountRemarkErrorCases(t *testing.T) {
	p := pool.New("")
	pn := New(Config{
		Pool:   p,
		APIKey: "testkey",
	})

	// 账号不存在 -> 404
	bodyBytes, _ := json.Marshal(map[string]string{"remark": "测试"})
	req := httptest.NewRequest(http.MethodPost, "/panel/api/accounts/nonexistent/remark", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer testkey")
	w := httptest.NewRecorder()
	pn.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}

	// 请求 JSON 格式错误 -> 400
	req2 := httptest.NewRequest(http.MethodPost, "/panel/api/accounts/u1/remark", bytes.NewReader([]byte("{invalid-json")))
	req2.Header.Set("Authorization", "Bearer testkey")
	w2 := httptest.NewRecorder()
	pn.ServeHTTP(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestVoucherStatusErrorCases(t *testing.T) {
	p := pool.New("")
	// 未配置 VoucherStore -> 501
	pn := New(Config{
		Pool:   p,
		APIKey: "testkey",
	})

	bodyBytes, _ := json.Marshal(map[string]any{"code": "VC-1", "is_used": true})
	req := httptest.NewRequest(http.MethodPost, "/panel/api/school/vouchers/status", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer testkey")
	w := httptest.NewRecorder()
	pn.ServeHTTP(w, req)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d: %s", w.Code, w.Body.String())
	}

	tmpDir := t.TempDir()
	vStore, _ := voucher.NewStore(filepath.Join(tmpDir, "vouchers.json"))
	pnWithStore := New(Config{
		Pool:         p,
		VoucherStore: vStore,
		APIKey:       "testkey",
	})

	// 券码为空 -> 400
	bodyEmptyCode, _ := json.Marshal(map[string]any{"code": "  ", "is_used": true})
	reqEmpty := httptest.NewRequest(http.MethodPost, "/panel/api/school/vouchers/status", bytes.NewReader(bodyEmptyCode))
	reqEmpty.Header.Set("Authorization", "Bearer testkey")
	wEmpty := httptest.NewRecorder()
	pnWithStore.ServeHTTP(wEmpty, reqEmpty)
	if wEmpty.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", wEmpty.Code, wEmpty.Body.String())
	}

	// 请求 JSON 格式错误 -> 400
	reqBadJSON := httptest.NewRequest(http.MethodPost, "/panel/api/school/vouchers/status", bytes.NewReader([]byte("{bad-json")))
	reqBadJSON.Header.Set("Authorization", "Bearer testkey")
	wBadJSON := httptest.NewRecorder()
	pnWithStore.ServeHTTP(wBadJSON, reqBadJSON)
	if wBadJSON.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", wBadJSON.Code, wBadJSON.Body.String())
	}
}

func TestSchoolVouchersEnrichedAndNotify(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/portal/activity/school/vouchers" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "OK",
				"data": map[string]any{
					"items": []map[string]any{
						{
							"grant_id":   1001,
							"sku_code":   "kfc_ice",
							"prize_name": "肯德基甜筒",
							"code":       "KFC-ICE-001",
							"valid_to":   "2026-10-31",
							"granted_at": "2026-09-22T10:00:00Z",
						},
						{
							"grant_id":   1002,
							"sku_code":   "kfc_burger",
							"prize_name": "肯德基汉堡",
							"code":       "KFC-BGR-002",
							"valid_to":   "2026-10-31",
							"granted_at": "2026-09-22T10:00:00Z",
						},
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	origTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = origTransport }()

	var tgMu sync.Mutex
	var tgSent []string
	http.DefaultTransport = &mockHTTPTransport{
		roundTrip: func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			tgMu.Lock()
			tgSent = append(tgSent, string(body))
			tgMu.Unlock()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		},
	}

	tmpDir := t.TempDir()
	authFile := filepath.Join(tmpDir, "workbuddy-u1.json")
	authJSON := `{"account":{"uid":"u1","nickname":"张三","remark":"主号"},"auth":{"accessToken":"at","refreshToken":"rt","domain":"www.codebuddy.cn","realm":"cn"}}`
	_ = os.WriteFile(authFile, []byte(authJSON), 0o600)
	a, err := auth.ParseFile(authFile)
	if err != nil {
		t.Fatal(err)
	}

	p := pool.New("")
	p.Add(a)

	vStore, err := voucher.NewStore(filepath.Join(tmpDir, "vouchers.json"))
	if err != nil {
		t.Fatal(err)
	}

	// 预先标记 KFC-BGR-002 为已使用
	_ = vStore.SetUsed("KFC-BGR-002", true, "u1", a.DisplayName(), "肯德基汉堡", "2026-10-31")

	up := &upstream.Client{
		HTTP:          server.Client(),
		BillingBaseCN: server.URL,
	}

	notifier := notify.NewNotifier(notify.Config{
		Enabled:  true,
		BotToken: "test-bot-token",
		ChatID:   "123456",
	})

	pn := New(Config{
		Pool:         p,
		Upstream:     up,
		VoucherStore: vStore,
		Notifier:     notifier,
		APIKey:       "testkey",
	})

	req := httptest.NewRequest(http.MethodGet, "/panel/api/school/vouchers", nil)
	req.Header.Set("Authorization", "Bearer testkey")
	w := httptest.NewRecorder()
	pn.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Accounts []struct {
			UID      string `json:"uid"`
			Nickname string `json:"nickname"`
			Vouchers []struct {
				Code      string `json:"code"`
				PrizeName string `json:"prize_name"`
				IsUsed    bool   `json:"is_used"`
			} `json:"vouchers"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(resp.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(resp.Accounts))
	}
	if len(resp.Accounts[0].Vouchers) != 2 {
		t.Fatalf("expected 2 vouchers, got %d", len(resp.Accounts[0].Vouchers))
	}

	// 验证 enrichment: KFC-BGR-002 应该是 IsUsed: true, KFC-ICE-001 应该是 IsUsed: false
	var foundUsed, foundUnused bool
	for _, vc := range resp.Accounts[0].Vouchers {
		if vc.Code == "KFC-BGR-002" && vc.IsUsed {
			foundUsed = true
		}
		if vc.Code == "KFC-ICE-001" && !vc.IsUsed {
			foundUnused = true
		}
	}
	if !foundUsed {
		t.Errorf("expected KFC-BGR-002 to be enriched as is_used=true")
	}
	if !foundUnused {
		t.Errorf("expected KFC-ICE-001 to be enriched as is_used=false")
	}

	// 等待异步 Telegram 推送 goroutine 完成
	time.Sleep(50 * time.Millisecond)

	tgMu.Lock()
	sentCount := len(tgSent)
	tgMu.Unlock()

	if sentCount < 1 {
		t.Errorf("expected at least 1 telegram notification, got %d", sentCount)
	}
}

func TestCheckAndNotifyVouchersConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	vStore, err := voucher.NewStore(filepath.Join(tmpDir, "vouchers.json"))
	if err != nil {
		t.Fatal(err)
	}

	origTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = origTransport }()

	var tgMu sync.Mutex
	var tgCount int
	http.DefaultTransport = &mockHTTPTransport{
		roundTrip: func(req *http.Request) (*http.Response, error) {
			tgMu.Lock()
			tgCount++
			tgMu.Unlock()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		},
	}

	notifier := notify.NewNotifier(notify.Config{
		Enabled:  true,
		BotToken: "test-bot-token",
		ChatID:   "123456",
	})

	pn := New(Config{
		VoucherStore: vStore,
		Notifier:     notifier,
	})

	a := &auth.Auth{
		UID:      "u1",
		Nickname: "并发测试",
	}

	vouchers := []upstream.SchoolVoucher{
		{
			Code:      "VC-CONC-1",
			PrizeName: "冰淇淋",
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pn.checkAndNotifyVouchers(a, vouchers)
		}()
	}
	wg.Wait()

	tgMu.Lock()
	finalCount := tgCount
	tgMu.Unlock()

	if finalCount != 1 {
		t.Errorf("expected exactly 1 notification due to mutex and FilterUnnotified, got %d", finalCount)
	}
}

func TestCheckAndNotifyVouchersSilentWhenDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	vStore, err := voucher.NewStore(filepath.Join(tmpDir, "vouchers.json"))
	if err != nil {
		t.Fatal(err)
	}

	pn := New(Config{
		VoucherStore: vStore,
		Notifier:     nil, // Nil notifier
	})

	a := &auth.Auth{
		UID:      "u1",
		Nickname: "静默测试",
	}

	vouchers := []upstream.SchoolVoucher{
		{
			Code:      "VC-SILENT-1",
			PrizeName: "咖啡",
		},
	}

	pn.checkAndNotifyVouchers(a, vouchers)

	// 应被静默标记为已通知
	unnotified := vStore.FilterUnnotified(vouchers)
	if len(unnotified) != 0 {
		t.Errorf("expected 0 unnotified vouchers after silent mark, got %d", len(unnotified))
	}
}
