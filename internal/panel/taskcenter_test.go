package panel

import (
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// TestGrowthPendingFiltersLocked 待办扫描必须过滤上游 locked 任务。
//
// 现场：Sequential 族每日零点解锁一环，做完一环后下一环以 locked=true 形态出现
// 在 mp 列表里。若扫进待办，队列执行时 accept 必然不落账（返回「未登记生效」），
// 用户视角就是「做完再扫还冒出来」。零点解锁后自动回到待办。
func TestGrowthPendingFiltersLocked(t *testing.T) {
	// 已登记 + 未达标 + 未领取 → 待办。
	pending := upstream.Task{TaskCode: "chat_5", Target: 5, Current: 1}
	if !growthPending(pending) {
		t.Error("未锁定/未达标的已登记任务应入待办")
	}

	// 同上但上游标记 locked → 不出待办。
	locked := pending
	locked.Locked = true
	if growthPending(locked) {
		t.Error("locked 任务不应入待办（accept 必不落账）")
	}

	// 已领取 → 不出待办（locked 与否同）。
	if growthPending(upstream.Task{TaskCode: "chat_5", Target: 5, Current: 5, Claimed: true}) {
		t.Error("已领取任务不应入待办")
	}

	// ⚠️ 达标未领：此处钉的是**实际行为**（不入队），与紧邻的注释
	// 「达标未领：也入队（队列执行后会自动领）」相反。
	// 该矛盾自 ce717b7（2026-09-14 引入任务中心）起即存在，两个 fork 均未修正，
	// 非本次改动引入。语义待定：若要按注释实现（改 return true），此断言需同步翻转。
	if growthPending(upstream.Task{TaskCode: "chat_5", Target: 5, Current: 5}) {
		t.Error("达标未领任务当前不入待办（与注释矛盾，见上）；若改为入队请同步此断言")
	}

	// 未登记动作的任务 → 不出待办（无法自动化）。
	if growthPending(upstream.Task{TaskCode: "Expert_Philanthropy", Target: 1, Current: 0}) {
		t.Error("无自动动作的任务不应入待办")
	}

	// 锁定判定优先于「无动作」判定之外的其余分支：未登记动作 + locked 同不出。
	if growthPending(upstream.Task{TaskCode: "Expert_Philanthropy", Target: 1, Locked: true}) {
		t.Error("无自动动作且 locked 的任务不应入待办")
	}
}
