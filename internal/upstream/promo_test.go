package upstream

import (
	"encoding/json"
	"testing"
	"time"
)

// promoFromJSON 按上游真实载荷形态构造 v3ModelPromotion（顺带校验 json tag）。
func promoFromJSON(t *testing.T, s string) v3ModelPromotion {
	t.Helper()
	var p v3ModelPromotion
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatalf("promo json 解析失败: %v\n%s", err, s)
	}
	return p
}

// TestPromoClock 解析 "HH:MM"：合法值转当日分钟数，坏值一律 (-1,false)。
func TestPromoClock(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"00:00", 0, true},
		{"23:00", 1380, true},
		{"7:50", 470, true},
		{" 09:05 ", 545, true}, // 上游可能带空白
		{"24:00", 1440, true},  // 跨午夜窗口的右端点用得到
		{"abc", -1, false},
		{"1:2:3", -1, false},
		{"25:00", -1, false},
		{"12:60", -1, false},
		{"", -1, false},
		{":", -1, false},
	} {
		got, ok := promoClock(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("promoClock(%q) = (%d,%v), want (%d,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// TestPromoActive 生效判定：enabled / validFrom-Until / daily 窗口（含跨午夜）。
//
// promoActive 是纯函数（now 显式传入），故这里不依赖墙上时钟、不会随 CI 跑的时刻飘。
func TestPromoActive(t *testing.T) {
	at := func(h, m int) time.Time {
		return time.Date(2026, 9, 23, h, m, 0, 0, promoZone)
	}
	for _, tc := range []struct {
		name string
		json string
		now  time.Time
		want bool
	}{
		{"disabled 恒不生效", `{"enabled":false}`, at(12, 0), false},
		{"无 schedule 全天生效", `{"enabled":true}`, at(3, 0), true},
		{"跨午夜窗口内（午夜前）", `{"enabled":true,"schedule":{"daily":[{"start":"23:00","end":"7:50"}]}}`, at(23, 30), true},
		{"跨午夜窗口内（午夜后）", `{"enabled":true,"schedule":{"daily":[{"start":"23:00","end":"7:50"}]}}`, at(3, 0), true},
		{"跨午夜边界-起点含", `{"enabled":true,"schedule":{"daily":[{"start":"23:00","end":"7:50"}]}}`, at(23, 0), true},
		{"跨午夜边界-终点不含", `{"enabled":true,"schedule":{"daily":[{"start":"23:00","end":"7:50"}]}}`, at(7, 50), false},
		{"跨午夜窗口外（白天）", `{"enabled":true,"schedule":{"daily":[{"start":"23:00","end":"7:50"}]}}`, at(12, 0), false},
		{"普通窗口内", `{"enabled":true,"schedule":{"daily":[{"start":"09:00","end":"17:00"}]}}`, at(12, 0), true},
		{"普通窗口边界-终点不含", `{"enabled":true,"schedule":{"daily":[{"start":"09:00","end":"17:00"}]}}`, at(17, 0), false},
		{"普通窗口外", `{"enabled":true,"schedule":{"daily":[{"start":"09:00","end":"17:00"}]}}`, at(20, 0), false},
		{"多窗口任一命中即生效", `{"enabled":true,"schedule":{"daily":[{"start":"08:00","end":"09:00"},{"start":"23:00","end":"23:30"}]}}`, at(23, 15), true},
		{"坏时段被跳过→窗口全不匹配", `{"enabled":true,"schedule":{"daily":[{"start":"bad","end":"worse"}]}}`, at(12, 0), false},
		{"validFrom 已过+validUntil 未到", `{"enabled":true,"schedule":{"validFrom":"2026-09-01T00:00:00+08:00","validUntil":"2026-10-01T00:00:00+08:00"}}`, at(12, 0), true},
		{"validFrom 在未来", `{"enabled":true,"schedule":{"validFrom":"2026-10-01T00:00:00+08:00"}}`, at(12, 0), false},
		{"validUntil 已过", `{"enabled":true,"schedule":{"validUntil":"2026-09-01T00:00:00+08:00"}}`, at(12, 0), false},
		{"坏时间串被忽略（不关掉优惠）", `{"enabled":true,"schedule":{"validFrom":"not-a-time","validUntil":"also-bad"}}`, at(12, 0), true},
	} {
		p := promoFromJSON(t, tc.json)
		if got := promoActive(&p, tc.now); got != tc.want {
			t.Errorf("%s: promoActive = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestApplyModelPromotions 挂载口径：同模型取 priority 最高；无 discount 的
// （错峰类）只挂标签、PromoFactor 留 nil；目录外模型不挂。
//
// 所有用例的时段都写成全天覆盖（00:00→24:00）或干脆不带 schedule，故结果与
// 墙上时钟无关——时段本身的判定已由 TestPromoActive 用显式 now 覆盖。
func TestApplyModelPromotions(t *testing.T) {
	out := map[string]ModelInfo{
		"hy4-preview-f": {ID: "hy4-preview-f", Credits: "x0.29"},
		"glm-5.2":       {ID: "glm-5.2", Credits: "x0.79"},
		"deepseek-4.1":  {ID: "deepseek-4.1", Credits: "x0.03"},
	}
	promos := []v3ModelPromotion{
		// 低优先级：badge-only 形态
		promoFromJSON(t, `{"enabled":true,"priority":50,"modelIds":["glm-5.2"],"badge":{"label":"夜间折扣"}}`),
		// 高优先级：夜间五折，应当胜出
		promoFromJSON(t, `{"enabled":true,"priority":100,"modelIds":["glm-5.2"],
			"badge":{"label":"夜间折扣"},"hover":{"textZh":"每日 23:00-07:50 五折"},
			"discount":{"factor":0.5,"discountedCredits":"0.50x"},
			"schedule":{"daily":[{"start":"00:00","end":"24:00"}],"timezone":"Asia/Shanghai"}}`),
		// 限时免费（factor=0）
		promoFromJSON(t, `{"enabled":true,"priority":10,"modelIds":["hy4-preview-f"],
			"badge":{"label":"限时免费"},"discount":{"factor":0,"discountedCredits":"0x"}}`),
		// 未启用 → 不得生效（priority 再高也不认）
		promoFromJSON(t, `{"enabled":false,"priority":999,"modelIds":["hy4-preview-f"],
			"badge":{"label":"不该出现"},"discount":{"factor":0.1,"discountedCredits":"0.10x"}}`),
		// 目录外模型 → 不挂（也不 panic）
		promoFromJSON(t, `{"enabled":true,"priority":1,"modelIds":["not-in-catalog"],"badge":{"label":"幽灵"}}`),
		// 错峰类：无 discount，只挂标签+说明
		promoFromJSON(t, `{"enabled":true,"priority":1,"modelIds":["deepseek-4.1"],
			"badge":{"label":"错峰使用"},"hover":{"textZh":"每日 00:00-08:00 错峰"}}`),
	}
	applyModelPromotions(out, promos)

	glm := out["glm-5.2"]
	if glm.PromoFactor == nil || *glm.PromoFactor != 0.5 {
		t.Errorf("glm-5.2 应取 priority 最高的五折，got factor=%v", glm.PromoFactor)
	}
	if glm.PromoCredits != "0.50x" || glm.PromoLabel != "夜间折扣" {
		t.Errorf("glm-5.2 promo=%q/%q, want 0.50x/夜间折扣", glm.PromoCredits, glm.PromoLabel)
	}
	if glm.Credits != "x0.79" {
		t.Errorf("牌价不得被优惠覆盖：Credits=%q want x0.79", glm.Credits)
	}

	f := out["hy4-preview-f"]
	if f.PromoFactor == nil || *f.PromoFactor != 0 {
		t.Errorf("限时免费 factor 应为 0（不是 nil），got %v", f.PromoFactor)
	}
	if f.PromoCredits != "0x" || f.PromoLabel != "限时免费" {
		t.Errorf("hy4-preview-f promo=%q/%q, want 0x/限时免费", f.PromoCredits, f.PromoLabel)
	}

	d := out["deepseek-4.1"]
	if d.PromoFactor != nil {
		t.Errorf("无 discount 的促销不应产生 factor，got %v", *d.PromoFactor)
	}
	if d.PromoLabel != "错峰使用" || d.PromoNote != "每日 00:00-08:00 错峰" {
		t.Errorf("错峰类应挂标签+说明，got %q/%q", d.PromoLabel, d.PromoNote)
	}

	if _, exists := out["not-in-catalog"]; exists {
		t.Error("目录外模型不得被优惠凭空造出来")
	}

	// 空输入是 no-op（不 panic、不改动）。
	empty := map[string]ModelInfo{"a": {ID: "a"}}
	applyModelPromotions(empty, nil)
	applyModelPromotions(nil, promos)
	if empty["a"].PromoLabel != "" {
		t.Error("空促销列表不应改动目录")
	}
}
