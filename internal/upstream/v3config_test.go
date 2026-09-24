package upstream

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestFetchV3ConfigModelMapTrialBannerAndPromotions 一次 /v3/config 解析的三件事：
//  1. data.models → 目录条目；
//  2. productFeaturesConfig.ModelTrialBanner → 补入「N 天免费试用」模型（data.models
//     里没有，漏了客户端就选不到），能力从 targetModelId 继承、Credits/Tags 清空；
//  3. modelPromotions → 按 now 评估后挂到条目上（牌价留在 Credits，生效价走 Promo*）。
//
// 促销目标故意选**由横幅补入**的 hy4-preview-f：这同时钉住了处理顺序
// （先补横幅、再挂促销），顺序反了这条促销就挂不上。
func TestFetchV3ConfigModelMapTrialBannerAndPromotions(t *testing.T) {
	var gotUA string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/v3/config") {
			t.Errorf("unexpected path %s", r.URL.Path)
			return jsonResp(404, `{}`), nil
		}
		gotUA = r.Header.Get("User-Agent")
		return jsonResp(200, `{"code":0,"data":{
			"models":[
				{"id":"hy4-preview","name":"Hy4 Preview","credits":"x0.29","tags":["badge:热门"],
				 "maxInputTokens":200000,"maxOutputTokens":32000,
				 "reasoning":{"defaultEffort":"high","supportedEfforts":["low","high"]}},
				{"id":"glm-5.2","name":"GLM-5.2","credits":"x0.79","maxInputTokens":100000,"maxOutputTokens":16000},
				{"id":"hy3-trial","name":"上游已下发","credits":"x9.99","maxInputTokens":50000,"maxOutputTokens":8000}
			],
			"productFeaturesConfig":{"ModelTrialBanner":{"banners":[
				{"firstUseTimeKey":"hy4.first_user_time","modelId":"hy4-preview-f","targetModelId":"hy4-preview","trialDays":14},
				{"modelId":"hy3-trial","targetModelId":"hy4-preview"},
				{"modelId":"orphan-trial","targetModelId":"no-such-target"},
				{"modelId":"","targetModelId":"hy4-preview"}
			]}},
			"modelPromotions":[
				{"enabled":true,"priority":10,"modelIds":["hy4-preview-f"],
				 "badge":{"label":"限时免费"},"hover":{"textZh":"新用户 14 天免费"},
				 "discount":{"factor":0,"discountedCredits":"0x"}},
				{"enabled":true,"priority":1,"modelIds":["glm-5.2"],
				 "badge":{"label":"错峰使用"},"hover":{"textZh":"每日 00:00-08:00 错峰"}}
			]
		}}`), nil
	})
	a := &auth.Auth{AccessToken: "at", UID: "u1", Domain: "copilot.tencent.com"}

	byID, err := c.fetchV3ConfigModelMap(a, codeBuddyCLIUA)
	if err != nil {
		t.Fatalf("fetchV3ConfigModelMap: %v", err)
	}
	if gotUA != codeBuddyCLIUA {
		t.Errorf("UA 参数未生效：got %q want %q", gotUA, codeBuddyCLIUA)
	}

	// 1) 常规条目原样在。
	if _, ok := byID["glm-5.2"]; !ok {
		t.Fatal("data.models 条目应入目录")
	}

	// 2a) 目录外的试用模型：能力从 targetModelId 继承，Credits/Tags 清空。
	f := byID["hy4-preview-f"]
	if f.ID != "hy4-preview-f" {
		t.Fatalf("试用模型应补入目录，got %+v", f)
	}
	if f.MaxTokens != 32000 || f.ContextWindow != 200000 {
		t.Errorf("能力应从 targetModelId 继承，got ctx=%d maxOut=%d", f.ContextWindow, f.MaxTokens)
	}
	if f.DefaultEffort != "high" || len(f.Efforts) != 2 {
		t.Errorf("推理档位应从 targetModelId 继承，got default=%q efforts=%v", f.DefaultEffort, f.Efforts)
	}
	if f.Credits != "" {
		t.Errorf("试用版 Credits 必须清空（那是转正后牌价），got %q", f.Credits)
	}
	if f.Tags != nil {
		t.Errorf("试用版 Tags 必须清空（那是转正后营销信息），got %v", f.Tags)
	}
	// 2b) data.models 已有的同 id **不被横幅覆盖**（data.models 权威）。
	if got := byID["hy3-trial"]; got.Name != "上游已下发" || got.Credits != "x9.99" || got.MaxTokens != 8000 {
		t.Errorf("data.models 已有同 id 时不应被横幅覆盖，got name=%q credits=%q maxOut=%d",
			got.Name, got.Credits, got.MaxTokens)
	}
	// 2c) target 不存在 → 只落 id，不编造能力字段。
	orphan := byID["orphan-trial"]
	if orphan.ID != "orphan-trial" || orphan.MaxTokens != 0 || orphan.Credits != "" || orphan.Tags != nil {
		t.Errorf("target 缺失时应只落 id，got %+v", orphan)
	}
	// 2d) modelId 为空的横幅被跳过。
	if _, ok := byID[""]; ok {
		t.Error("空 modelId 不应入目录")
	}

	// 3) 优惠：有 discount 的挂生效价（含横幅补入的条目），无 discount 的只挂标签。
	if hy := byID["hy4-preview"]; hy.PromoFactor != nil || hy.PromoLabel != "" {
		t.Errorf("未被促销命中的模型不应带优惠，got %+v", hy)
	}
	if f.PromoFactor == nil || *f.PromoFactor != 0 {
		t.Errorf("横幅补入的模型也应挂上促销，got factor=%v", f.PromoFactor)
	}
	if f.PromoCredits != "0x" || f.PromoLabel != "限时免费" || f.PromoNote != "新用户 14 天免费" {
		t.Errorf("hy4-preview-f promo=%q/%q/%q", f.PromoCredits, f.PromoLabel, f.PromoNote)
	}
	glm := byID["glm-5.2"]
	if glm.PromoLabel != "错峰使用" || glm.PromoNote != "每日 00:00-08:00 错峰" {
		t.Errorf("glm-5.2 应挂标签+说明，got %q/%q", glm.PromoLabel, glm.PromoNote)
	}
	if glm.PromoFactor != nil {
		t.Errorf("错峰类无 discount，factor 应为 nil，got %v", *glm.PromoFactor)
	}
	if glm.Credits != "x0.79" {
		t.Errorf("牌价不得被覆盖，got %q", glm.Credits)
	}
}

// globalProbeClient 构造一个只探 global 的客户端：/v3/config 走 handler，
// 企业端点家族一律 404（探测降级为「仅 v3」，从而把双 UA 并集行为单独隔离出来）。
func globalProbeClient(t *testing.T, v3 func(ua string) (int, string)) (*Client, *[]string) {
	t.Helper()
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	var mu sync.Mutex
	seenUA := []string{}
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/v3/config") {
			ua := r.Header.Get("User-Agent")
			mu.Lock()
			seenUA = append(seenUA, ua)
			mu.Unlock()
			status, body := v3(ua)
			return jsonResp(status, body), nil
		}
		// 企业端点家族：全部 404 → probeGlobalModels 走「仅 v3」降级路径。
		return jsonResp(404, `{}`), nil
	})
	c.ChatBaseGlobal = "https://global.example"
	c.GlobalEnabled = true
	return c, &seenUA
}

// TestProbeGlobalModelsDualUAUnion /v3/config 对不同 UA 下发不同模型集合，
// 两路并发取并集——只走一路必然缺模型（实测 IDE 无 deepseek 系列、CLI 无 o4-mini）。
func TestProbeGlobalModelsDualUAUnion(t *testing.T) {
	c, seenUA := globalProbeClient(t, func(ua string) (int, string) {
		switch ua {
		case codeBuddyIDEUA:
			return 200, `{"code":0,"data":{"models":[
				{"id":"o4-mini","maxInputTokens":200000,"maxOutputTokens":32000},
				{"id":"auto-chat","maxInputTokens":100000,"maxOutputTokens":16000}]}}`
		case codeBuddyCLIUA:
			return 200, `{"code":0,"data":{"models":[
				{"id":"deepseek-v4.1-flash","maxInputTokens":1000000,"maxOutputTokens":128000},
				{"id":"gpt-6-astra","maxInputTokens":400000,"maxOutputTokens":64000}]}}`
		default:
			return 400, `{"code":12403,"msg":"unknown ua"}`
		}
	})
	acct := &auth.Auth{UID: "g1", AccessToken: "at", Domain: "www.workbuddy.ai"}

	names, infos, _, _, err := c.probeGlobalModels(acct)
	if err != nil {
		t.Fatalf("probeGlobalModels: %v", err)
	}
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	for _, want := range []string{"o4-mini", "auto-chat", "deepseek-v4.1-flash", "gpt-6-astra"} {
		if !got[want] {
			t.Errorf("并集缺少 %q（只有单路才会缺），got %v", want, names)
		}
	}
	if len(infos) != 4 {
		t.Errorf("infos 应为并集 4 条，got %d", len(infos))
	}

	// 两路都必须真的发出去（只发一路时并集必然不完整）。
	uas := map[string]int{}
	for _, u := range *seenUA {
		uas[u]++
	}
	if uas[codeBuddyIDEUA] != 1 || uas[codeBuddyCLIUA] != 1 {
		t.Errorf("应各发一路 v3/config，got %v", uas)
	}
}

// TestProbeGlobalModelsSingleUAFailureDegrades 单路失败降级为另一路（不拖垮整体）：
// IDE 路 500、CLI 路正常 → 结果就是 CLI 路，且不返回错误。
func TestProbeGlobalModelsSingleUAFailureDegrades(t *testing.T) {
	c, _ := globalProbeClient(t, func(ua string) (int, string) {
		if ua == codeBuddyIDEUA {
			return 500, `boom`
		}
		return 200, `{"code":0,"data":{"models":[
			{"id":"deepseek-v4.1-flash","maxInputTokens":1000000,"maxOutputTokens":128000}]}}`
	})
	acct := &auth.Auth{UID: "g1", AccessToken: "at", Domain: "www.workbuddy.ai"}

	names, infos, _, _, err := c.probeGlobalModels(acct)
	if err != nil {
		t.Fatalf("单路失败应降级而非报错，got %v", err)
	}
	if len(names) != 1 || names[0] != "deepseek-v4.1-flash" {
		t.Errorf("应退化为 CLI 路结果，got %v", names)
	}
	if len(infos) != 1 || infos[0].MaxTokens != 128000 {
		t.Errorf("退化结果应保留 CLI 路字段，got %+v", infos)
	}
}

// TestProbeGlobalModelsBothUAFail 两路 v3 全失败 + 企业端点也失败 → 返回错误
// （负缓存语义，与改动前一致）。
func TestProbeGlobalModelsBothUAFail(t *testing.T) {
	c, _ := globalProbeClient(t, func(ua string) (int, string) {
		return 500, `boom`
	})
	acct := &auth.Auth{UID: "g1", AccessToken: "at", Domain: "www.workbuddy.ai"}

	if _, _, _, _, err := c.probeGlobalModels(acct); err == nil {
		t.Fatal("两路全失败应返回错误（负缓存语义）")
	}
}
