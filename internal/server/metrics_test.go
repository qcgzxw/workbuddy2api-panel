package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// resetMetricsForTest 隔离用例间的全局聚合状态。
func resetMetricsForTest(t *testing.T) {
	t.Helper()
	ResetMetrics()
	t.Cleanup(ResetMetrics)
}

// TestMetricsAggregatesByModel 同一模型的多次请求累加，派生字段按口径折算。
func TestMetricsAggregatesByModel(t *testing.T) {
	resetMetricsForTest(t)

	// 两次成功 + 一次失败，同一模型。
	mk := func(status int, ttfbMS, toks, prompt, hit, miss, wr int, credit float64, mode string) *chatStat {
		return &chatStat{
			model: "global:deepseek-v4.1-flash", mode: mode, status: status,
			ttfb: time.Duration(ttfbMS) * time.Millisecond,
			toks: toks, hasUsage: true, prompt: prompt,
			cacheHit: hit, cacheMiss: miss, cacheWr: wr,
			credit: credit, hasCredit: true,
		}
	}
	recordChatMetric(mk(200, 1000, 100, 50, 800, 200, 0, 0.02, "stream"), 5*time.Second)
	recordChatMetric(mk(200, 2000, 200, 60, 900, 100, 0, 0.04, "stream"), 7*time.Second)
	recordChatMetric(mk(429, 0, -1, 0, 0, 0, 0, 0, "sync"), 1*time.Second)

	snap := MetricsSnapshotOf()
	if len(snap.Models) != 1 {
		t.Fatalf("models=%d want 1", len(snap.Models))
	}
	m := snap.Models[0]
	if m.Requests != 3 || m.Success != 2 || m.Failed != 1 {
		t.Errorf("req/succ/fail = %d/%d/%d want 3/2/1", m.Requests, m.Success, m.Failed)
	}
	if m.Streaming != 2 {
		t.Errorf("streaming=%d want 2", m.Streaming)
	}
	// 端到端均值 = (5000+7000+1000)/3 = 4333.33ms
	if got := m.AvgLatencyMS; got < 4333 || got > 4334 {
		t.Errorf("avg_latency=%.2f want ~4333.33", got)
	}
	// TTFB 只统计有观测的两次：(1000+2000)/2 = 1500ms
	if got := m.AvgTTFBMS; got != 1500 {
		t.Errorf("avg_ttfb=%.2f want 1500", got)
	}
	// token 只累加 hasUsage 的两次
	if m.PromptTokens != 110 || m.CompletionTokens != 300 {
		t.Errorf("prompt/comp = %d/%d want 110/300", m.PromptTokens, m.CompletionTokens)
	}
	// 命中率 = 1700/(1700+300) = 0.85
	if got := m.CacheHitRate; got < 0.8499 || got > 0.8501 {
		t.Errorf("cache_hit_rate=%.4f want 0.85", got)
	}
	if m.Credit != 0.06 {
		t.Errorf("credit=%.4f want 0.06", m.Credit)
	}
}

// TestMetricsMissingUsageNotCountedAsZero usage 缺失时不得把 0 计进 token/缓存。
func TestMetricsMissingUsageNotCountedAsZero(t *testing.T) {
	resetMetricsForTest(t)

	// hasUsage=false（上游没回 usage）：toks=-1 是哨兵，不该被当成 token 累加。
	recordChatMetric(&chatStat{
		model: "m1", mode: "sync", status: 200, toks: -1,
	}, time.Second)
	// hasUsage=true 且显式全 0：合法观测，参与累加（分母不为零才有意义）。
	recordChatMetric(&chatStat{
		model: "m1", mode: "sync", status: 200, toks: 0, hasUsage: true,
	}, time.Second)

	snap := MetricsSnapshotOf()
	m := snap.Models[0]
	if m.Requests != 2 {
		t.Fatalf("requests=%d want 2", m.Requests)
	}
	if m.CompletionTokens != 0 {
		t.Errorf("completion=%d want 0（-1 哨兵不得计入）", m.CompletionTokens)
	}
	if m.CacheHitRate != 0 {
		t.Errorf("cache_hit_rate=%f want 0（无观测时不做除法）", m.CacheHitRate)
	}
}

// TestMetricsTotalIsSumOfModels total 必须等于各模型累加，不另算一份。
func TestMetricsTotalIsSumOfModels(t *testing.T) {
	resetMetricsForTest(t)

	recordChatMetric(&chatStat{model: "a", mode: "sync", status: 200, toks: 10, hasUsage: true, prompt: 5}, time.Second)
	recordChatMetric(&chatStat{model: "b", mode: "sync", status: 500, toks: 20, hasUsage: true, prompt: 7}, time.Second)

	snap := MetricsSnapshotOf()
	if snap.Total.Requests != 2 || snap.Total.Success != 1 || snap.Total.Failed != 1 {
		t.Errorf("total req/succ/fail = %d/%d/%d want 2/1/1",
			snap.Total.Requests, snap.Total.Success, snap.Total.Failed)
	}
	if snap.Total.PromptTokens != 12 || snap.Total.CompletionTokens != 30 {
		t.Errorf("total tokens = %d/%d want 12/30", snap.Total.PromptTokens, snap.Total.CompletionTokens)
	}
}

// TestMetricsResetClears 重置后归零。
func TestMetricsResetClears(t *testing.T) {
	resetMetricsForTest(t)

	recordChatMetric(&chatStat{model: "a", mode: "sync", status: 200, toks: 10, hasUsage: true}, time.Second)
	if MetricsSnapshotOf().Total.Requests != 1 {
		t.Fatal("前置：应有 1 条")
	}
	ResetMetrics()
	snap := MetricsSnapshotOf()
	if snap.Total.Requests != 0 || len(snap.Models) != 0 {
		t.Errorf("重置后 requests=%d models=%d want 0/0", snap.Total.Requests, len(snap.Models))
	}
}

// TestMetricsEmptyModelFallsBack 空模型名归入 "-"，不丢弃观测。
func TestMetricsEmptyModelFallsBack(t *testing.T) {
	resetMetricsForTest(t)

	recordChatMetric(&chatStat{model: "", mode: "sync", status: 200}, time.Second)
	snap := MetricsSnapshotOf()
	if len(snap.Models) != 1 || snap.Models[0].Model != "-" {
		t.Fatalf("空模型名应归入 \"-\"，得到 %+v", snap.Models)
	}
	if snap.Total.Requests != 1 {
		t.Errorf("观测不得因模型名为空而丢弃")
	}
}

// TestMetricsCapBounded 超容量上限时丢弃新键且不 panic。
func TestMetricsCapBounded(t *testing.T) {
	resetMetricsForTest(t)

	for i := 0; i < metricsCap+50; i++ {
		recordChatMetric(&chatStat{model: string(rune('a'+i%26)) + string(rune('0'+i%10)) + string(rune('A'+i/260)), mode: "sync", status: 200}, time.Second)
	}
	snap := MetricsSnapshotOf()
	if len(snap.Models) > metricsCap {
		t.Errorf("models=%d 超过上限 %d", len(snap.Models), metricsCap)
	}
}

// TestStatsEndpointAndReset /v1/stats 返回聚合载荷（含 total 行），reset 清空。
func TestStatsEndpointAndReset(t *testing.T) {
	resetMetricsForTest(t)

	recordChatMetric(&chatStat{
		model: "cn:glm-5.2", mode: "stream", status: 200,
		toks: 7, hasUsage: true, prompt: 3, cacheHit: 10, cacheMiss: 5,
	}, 2*time.Second)

	h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/stats code=%d body=%s", rec.Code, rec.Body)
	}
	var snap MetricsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body)
	}
	if !snap.Enabled || snap.Since.IsZero() {
		t.Errorf("enabled=%v since=%v want true/非零", snap.Enabled, snap.Since)
	}
	if snap.Total.Requests != 1 || len(snap.Models) != 1 || snap.Models[0].Model != "cn:glm-5.2" {
		t.Fatalf("载荷不符：total=%+v models=%+v", snap.Total, snap.Models)
	}
	if snap.Models[0].CacheHitTokens != 10 || snap.Models[0].CacheMissTokens != 5 {
		t.Errorf("cache 三段未透出：%+v", snap.Models[0])
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("POST", "/v1/stats/reset", nil))
	if rec2.Code != http.StatusOK || !jsonHasOK(t, rec2.Body.Bytes()) {
		t.Fatalf("reset code=%d body=%s", rec2.Code, rec2.Body)
	}
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, httptest.NewRequest("GET", "/v1/stats", nil))
	var after MetricsSnapshot
	if err := json.Unmarshal(rec3.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after.Total.Requests != 0 || len(after.Models) != 0 {
		t.Errorf("reset 后 requests=%d models=%d want 0/0", after.Total.Requests, len(after.Models))
	}
}

// jsonHasOK 断言 {"ok":true} 形态。
func jsonHasOK(t *testing.T, raw []byte) bool {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, raw)
	}
	ok, _ := m["ok"].(bool)
	return ok
}

// sseWithCacheUsage 末帧带 usage 全字段（token 三段 + 缓存三段 + credit）的流式响应体。
const sseWithCacheUsage = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"total_tokens\":14,\"credit\":0.03,\"prompt_cache_hit_tokens\":800,\"prompt_cache_miss_tokens\":200,\"prompt_cache_write_tokens\":50}}\n\n" +
	"data: [DONE]\n\n"

// TestStatsCollectsFromChatPaths 埋点接线（handler 侧）：流式与非流式两条路径的观测
// 都必须进聚合表——唯一埋点是 chatStat.done()，本用例证明两条路径的字段填充都到位，
// 且缓存三段能穿过非流式的 Aggregate 路径（聚合响应的 usage 原样带出）。
func TestStatsCollectsFromChatPaths(t *testing.T) {
	resetMetricsForTest(t)
	acct := &auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}

	// 流式路径
	h := NewHandler(Config{
		Pool:     testPoolWith(acct),
		Upstream: newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseWithCacheUsage, true }),
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("流式 code=%d body=%s", rec.Code, rec.Body)
	}

	// 非流式路径（fake 同样回 SSE：非流式走 Aggregate 解析末帧 usage）
	h2 := NewHandler(Config{
		Pool:     testPoolWith(acct),
		Upstream: newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseWithCacheUsage, true }),
	})
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	if rec2.Code != http.StatusOK {
		t.Fatalf("非流式 code=%d body=%s", rec2.Code, rec2.Body)
	}

	snap := MetricsSnapshotOf()
	if snap.Total.Requests != 2 || snap.Total.Success != 2 || snap.Total.Failed != 0 {
		t.Fatalf("total req/succ/fail = %d/%d/%d want 2/2/0",
			snap.Total.Requests, snap.Total.Success, snap.Total.Failed)
	}
	if snap.Total.Streaming != 1 {
		t.Errorf("streaming=%d want 1", snap.Total.Streaming)
	}
	if snap.Total.PromptTokens != 20 || snap.Total.CompletionTokens != 8 || snap.Total.TotalTokens != 28 {
		t.Errorf("token 三段 = %d/%d/%d want 20/8/28",
			snap.Total.PromptTokens, snap.Total.CompletionTokens, snap.Total.TotalTokens)
	}
	if snap.Total.CacheHitTokens != 1600 || snap.Total.CacheMissTokens != 400 || snap.Total.CacheWriteTokens != 100 {
		t.Errorf("缓存三段 = %d/%d/%d want 1600/400/100",
			snap.Total.CacheHitTokens, snap.Total.CacheMissTokens, snap.Total.CacheWriteTokens)
	}
	if got := snap.Total.CacheHitRate; got < 0.7999 || got > 0.8001 {
		t.Errorf("cache_hit_rate=%.4f want 0.8（分母不含 write）", got)
	}
	if got := snap.Total.Credit; got < 0.0599 || got > 0.0601 {
		t.Errorf("credit=%.4f want 0.06", got)
	}
	// 不断言 TTFB 均值：fake 上游零延迟，首帧耗时可能 <1ms（`.Milliseconds()` 归零），
	// 均值恒为 0 没有区分力；TTFB 的累加口径由 TestMetricsAggregatesByModel 显式钉住。
}

// TestEnrichCreditsMergesReadOnlyCatalogs 倍率透出：CN/global 两域按 realm 各自查表，
// 目录冷/未下发 → 省略（缺失≠免费，不编造 "x0.00"），且不发起任何上游调用。
func TestEnrichCreditsMergesReadOnlyCatalogs(t *testing.T) {
	resetMetricsForTest(t)

	// ── CN 侧：直接写包级目录缓存（同包测试；TTL 内快照才被 cachedModelsSnapshot 采信）──
	dynamicModelsCache.Lock()
	oldIDs, oldFetched, oldFail := dynamicModelsCache.ids, dynamicModelsCache.fetched, dynamicModelsCache.lastFail
	dynamicModelsCache.ids = []upstream.ModelInfo{{ID: "glm-5.2", Credits: "x0.05"}}
	dynamicModelsCache.fetched = time.Now()
	dynamicModelsCache.lastFail = time.Time{}
	dynamicModelsCache.Unlock()
	t.Cleanup(func() {
		dynamicModelsCache.Lock()
		dynamicModelsCache.ids, dynamicModelsCache.fetched, dynamicModelsCache.lastFail = oldIDs, oldFetched, oldFail
		dynamicModelsCache.Unlock()
	})

	// ── global 侧：fake 上游预热一次（快照只用预热后的缓存），随后停服确保零新调用 ──
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"models":[{"id":"hy3","name":"Hy3","credits":"x0.7"}]}}`))
	}))
	up := upstream.New()
	up.ChatBaseGlobal = srv.URL
	up.GlobalEnabled = true
	if got := up.FetchGlobalModelInfos(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"}); len(got) != 1 {
		t.Fatalf("预热 global 目录失败：%+v", got)
	}
	srv.Close() // 关掉服务：后续任何上游调用都会失败 → 反证"倍率透出只读"

	h := NewHandler(Config{Pool: testPoolWith(), Upstream: up})

	recordChatMetric(&chatStat{model: "cn:glm-5.2", mode: "sync", status: 200, hasUsage: true}, time.Second)
	recordChatMetric(&chatStat{model: "global:hy3", mode: "sync", status: 200, hasUsage: true}, time.Second)
	recordChatMetric(&chatStat{model: "cn:not-in-catalog", mode: "sync", status: 200, hasUsage: true}, time.Second)

	snap := MetricsSnapshotOf()
	h.enrichCredits(&snap)

	got := map[string]string{}
	for _, m := range snap.Models {
		got[m.Model] = m.Credits
	}
	if got["cn:glm-5.2"] != "x0.05" {
		t.Errorf("CN 倍率 = %q want x0.05", got["cn:glm-5.2"])
	}
	if got["global:hy3"] != "x0.7" {
		t.Errorf("global 倍率 = %q want x0.7", got["global:hy3"])
	}
	if got["cn:not-in-catalog"] != "" {
		t.Errorf("目录未下发的模型应省略倍率（缺失≠免费），得到 %q", got["cn:not-in-catalog"])
	}
	// total 行不参与倍率合入（跨倍率聚合无意义）。
	if snap.Total.Credits != "" {
		t.Errorf("total 行不该有倍率，得到 %q", snap.Total.Credits)
	}
}
