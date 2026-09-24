package panel

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// offlineHTTP 一个不发网络的 http.Client（models.dev 兜底查询直接 404）。
func offlineHTTP() *http.Client {
	return &http.Client{Transport: &mockHTTPTransport{roundTrip: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 404,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	}}}
}

// TestPanelModelEntryPromoFields panelModelEntry 必须把优惠透出为 promo_* 四个键，
// 且与前端 rateCell 读的键名严格一致——这是跨语言契约，改名会静默降级为「只显示牌价」，
// 两边都不会报错。
//
// 另外钉住两点：credits 是**牌价**、不得被优惠覆盖；无优惠时四个键**都不出现**
// （前端用 promo_factor != null 判定，键存在但为 null 会让它误判）。
func TestPanelModelEntryPromoFields(t *testing.T) {
	httpc := offlineHTTP()

	// 有折扣（限时免费 factor=0）：四个键齐全，credits 保留牌价。
	factor := 0.0
	mi := upstream.ModelInfo{
		ID: "hy4-preview-f", Name: "Hy4 Preview", Credits: "x0.29",
		PromoFactor: &factor, PromoCredits: "0x", PromoLabel: "限时免费", PromoNote: "新用户 14 天免费",
	}
	e := panelModelEntry("global", mi, nil, "", httpc)
	if e["promo_factor"] != 0.0 {
		t.Errorf("promo_factor 应为 0（不是缺失/null），got %#v", e["promo_factor"])
	}
	if e["promo_credits"] != "0x" {
		t.Errorf("promo_credits=%v want 0x", e["promo_credits"])
	}
	if e["promo_label"] != "限时免费" {
		t.Errorf("promo_label=%v want 限时免费", e["promo_label"])
	}
	if e["promo_note"] != "新用户 14 天免费" {
		t.Errorf("promo_note=%v want 新用户 14 天免费", e["promo_note"])
	}
	if e["credits"] != "x0.29" {
		t.Errorf("credits 是牌价，不得被优惠覆盖，got %v", e["credits"])
	}
	if e["id"] != "global:hy4-preview-f" {
		t.Errorf("id 应带 realm 前缀，got %v", e["id"])
	}

	// 无优惠：四个键都不应出现。
	plain := panelModelEntry("cn", upstream.ModelInfo{ID: "glm-5.2", Credits: "x0.79"}, nil, "", httpc)
	for _, k := range []string{"promo_factor", "promo_credits", "promo_label", "promo_note"} {
		if _, ok := plain[k]; ok {
			t.Errorf("无优惠时不应出现键 %q（got %v）", k, plain[k])
		}
	}

	// 错峰类（只有标签、无 discount）：不得出现 promo_factor/promo_credits，
	// 否则前端会当成有生效价、显示成「生效价 + 划线牌价」的错误形态。
	badge := panelModelEntry("cn", upstream.ModelInfo{
		ID: "deepseek-v4.1", Credits: "x0.03", PromoLabel: "错峰使用", PromoNote: "每日 00:00-08:00",
	}, nil, "", httpc)
	if _, ok := badge["promo_factor"]; ok {
		t.Errorf("错峰类无 discount，不应有 promo_factor（got %v）", badge["promo_factor"])
	}
	if _, ok := badge["promo_credits"]; ok {
		t.Errorf("错峰类无 discount，不应有 promo_credits（got %v）", badge["promo_credits"])
	}
	if badge["promo_label"] != "错峰使用" || badge["promo_note"] != "每日 00:00-08:00" {
		t.Errorf("错峰类应透出标签+说明，got %v/%v", badge["promo_label"], badge["promo_note"])
	}
}
