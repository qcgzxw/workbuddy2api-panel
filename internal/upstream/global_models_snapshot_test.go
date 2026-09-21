package upstream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestGlobalModelInfosSnapshotReadOnly 只读快照的四个契约（吸收上游 1dfe750，#176）：
//   - (a) 冷客户端（从未探测）→ nil，且**零上游请求**（绝不主动探测）；
//   - (b) 预热后（FetchGlobalModelInfos 触发探测）→ 快照返回同一批全字段 infos（含 credits 原文）；
//   - (c) 反复读快照 → fake 请求计数不变（只读语义，与 Fetch*「miss 即探测」相反）；
//   - (d) TTL 过期 → nil（不把陈旧目录当成现状）。
func TestGlobalModelInfosSnapshotReadOnly(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"models":[{"id":"hy3","name":"Hy3","credits":"x0.05"}]}}`)
	}))
	defer srv.Close()

	c := New()
	c.ChatBaseGlobal = srv.URL
	c.GlobalEnabled = true
	acct := &auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"}
	callsNow := func() int { mu.Lock(); defer mu.Unlock(); return calls }

	// (a) 冷：快照 nil，零上游请求。
	if got := c.GlobalModelInfosSnapshot(); got != nil {
		t.Fatalf("冷启动快照 = %+v，want nil", got)
	}
	if n := callsNow(); n != 0 {
		t.Fatalf("冷快照发起了 %d 次上游请求，want 0（只读不得探测）", n)
	}

	// (b) 预热：探测一次 → 快照返回同批 infos。
	infos := c.FetchGlobalModelInfos(acct)
	if len(infos) != 1 || infos[0].ID != "hy3" || infos[0].Credits != "x0.05" {
		t.Fatalf("预热后 FetchGlobalModelInfos = %+v，want [{hy3 x0.05}]", infos)
	}
	snap := c.GlobalModelInfosSnapshot()
	if len(snap) != 1 || snap[0].ID != "hy3" || snap[0].Credits != "x0.05" {
		t.Fatalf("快照 = %+v，want 与探测结果同批", snap)
	}
	warmCalls := callsNow()
	if warmCalls == 0 {
		t.Fatal("预热应至少发生一次上游请求（前置不成立）")
	}

	// (c) 只读：再读 N 次零新请求。
	for i := 0; i < 5; i++ {
		if got := c.GlobalModelInfosSnapshot(); got == nil || got[0].ID != "hy3" {
			t.Fatalf("第 %d 次读快照 = %+v，want 缓存内容", i, got)
		}
	}
	if n := callsNow(); n != warmCalls {
		t.Errorf("反复读快照新增了 %d 次上游请求，want 0（只读）", n-warmCalls)
	}

	// (d) TTL 过期：把 fetched 拨回 2h 前（> globalModelsTTL）→ 快照 nil，且不触发探测。
	c.globalModels.Lock()
	c.globalModels.fetched = time.Now().Add(-2 * time.Hour)
	c.globalModels.Unlock()
	if got := c.GlobalModelInfosSnapshot(); got != nil {
		t.Errorf("TTL 过期后快照 = %+v，want nil（不返回陈旧目录）", got)
	}
	if n := callsNow(); n != warmCalls {
		t.Errorf("过期读快照发起了 %d 次新请求，want 0（只读不得借此探测）", n-warmCalls)
	}
}
