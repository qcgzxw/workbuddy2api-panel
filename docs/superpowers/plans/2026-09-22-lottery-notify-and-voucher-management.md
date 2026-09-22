# 抽奖中奖 Telegram 提醒、券码状态管理与账号文字备注 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现 WorkBuddy 抽奖中奖自动通过 Telegram Bot 发送包含券码/有效期/账号备注的消息，支持在 Web 面板与本地持久化中管理券码已使用状态，并为手机账号增加文字备注支持。

**Architecture:** 解耦设计，新增 `internal/notify` 处理 Telegram MarkdownV2 消息安全格式化与出站推送，新增 `internal/voucher` 负责本地 `data/vouchers.json` 原子持久化与状态管理；在 `internal/auth` 与 `internal/pool` 中扩充账号文字备注与统一展示名；调度器与面板在抽奖或查询券码时触发增量检测与推送，Web 前端提供交互切换。

**Tech Stack:** Go 1.26 (std net/http, sync, os, json), Vanilla JavaScript, HTML5/CSS3.

---

## 文件结构规划

- **新建文件**：
  - `internal/voucher/store.go`: 券码持久化仓库 `Store`，读写 `data/vouchers.json`，原子落盘、去重与 enrichment。
  - `internal/voucher/store_test.go`: `Store` 单元测试（加载、标记、过滤、并发落盘）。
  - `internal/notify/telegram.go`: Telegram Bot 发送客户端、MarkdownV2 转义防御、HTTP 超时与错误处理。
  - `internal/notify/telegram_test.go`: `notify` 单元测试（MarkdownV2 字符转义、mock HTTP 响应、网络错误处理）。
  - `internal/panel/voucher_api_test.go`: 面板券码状态与账号备注 API 单元测试。
- **修改文件**：
  - `internal/auth/auth.go`: `Auth` 增加 `Remark`、双形态（嵌套/扁平）解析、`saveAtomicLocked()` 防死锁与 `DisplayName()`。
  - `internal/auth/auth_test.go`: 凭据备注解析与写回测试。
  - `internal/pool/entry.go` & `internal/pool/state.go`: `Status` 增加 `Remark` 与 `DisplayName` 暴露。
  - `cmd/server/config.go`: `Config` 增加 `Telegram` 与 `VoucherFile` 字段及默认值。
  - `cmd/server/main.go`: 装配 `voucher.Store` 与 `notify.Notifier`，注入调度器和面板，更新重启提示。
  - `config.example.json`: 增加配置示例。
  - `internal/scheduler/scheduler.go` & `internal/scheduler/school.go`: 调度器开学季抽奖完成后的增量检测与 Telegram 推送。
  - `internal/panel/panel.go` & `internal/panel/taskcenter.go`: 增加 `/panel/api/accounts/{uid}/remark`、更新 `/panel/api/school/vouchers` 附加状态与增量通知、增加 `/panel/api/school/vouchers/status`。
  - `internal/panel/index.html`: 增加「隐藏已使用」切换控制。
  - `internal/panel/app.js`: 券码卡片「标记已使用/恢复未使用」动态更新，账号列表文字备注显示与在线修改。

---

### Task 1: 账号文字备注支持（Auth 结构、持久化、DisplayName 与单元测试）

**Files:**
- Modify: `internal/auth/auth.go:30-46,220-330`
- Test: `internal/auth/auth_test.go`
- Modify: `internal/pool/entry.go:63-102`
- Modify: `internal/pool/state.go:459-480`

- [ ] **Step 1: Write the failing tests in `internal/auth/auth_test.go`**

```go
func TestAuthRemarkAndDisplayName(t *testing.T) {
	// 1. 测试嵌套形凭据解析 remark
	nestedJSON := `{
		"account": {
			"uid": "u123",
			"nickname": "13800000000",
			"remark": "张三主号"
		},
		"auth": {
			"accessToken": "test-at",
			"refreshToken": "test-rt",
			"expiresAt": 1800000000,
			"domain": "www.codebuddy.cn",
			"realm": "cn"
		}
	}`
	a, err := Parse([]byte(nestedJSON))
	if err != nil {
		t.Fatalf("parse nested auth failed: %v", err)
	}
	if a.Remark != "张三主号" {
		t.Errorf("expected remark '张三主号', got %q", a.Remark)
	}
	if a.DisplayName() != "张三主号 (13800000000)" {
		t.Errorf("expected display name '张三主号 (13800000000)', got %q", a.DisplayName())
	}

	// 2. 测试扁平形凭据解析 remark
	flatJSON := `{
		"uid": "u456",
		"nickname": "13900000000",
		"remark": "备用号",
		"accessToken": "test-at",
		"refreshToken": "test-rt",
		"expiresAt": 1800000000,
		"domain": "www.codebuddy.cn",
		"realm": "cn"
	}`
	aFlat, err := Parse([]byte(flatJSON))
	if err != nil {
		t.Fatalf("parse flat auth failed: %v", err)
	}
	if aFlat.Remark != "备用号" {
		t.Errorf("expected remark '备用号', got %q", aFlat.Remark)
	}

	// 3. 测试无 remark 时的 DisplayName 回退
	aEmptyRemark := &Auth{Nickname: "13700000000", UID: "abcdef123456"}
	if aEmptyRemark.DisplayName() != "13700000000" {
		t.Errorf("expected nickname display name, got %q", aEmptyRemark.DisplayName())
	}
	aEmptyAll := &Auth{UID: "abcdef123456789"}
	if aEmptyAll.DisplayName() != "abcdef12" {
		t.Errorf("expected truncated uid fallback, got %q", aEmptyAll.DisplayName())
	}

	// 4. 测试 SetRemark 原子写回与防死锁
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "workbuddy-test.json")
	if err := os.WriteFile(filePath, []byte(nestedJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	a.FilePath = filePath
	if err := a.SetRemark("新备注名"); err != nil {
		t.Fatalf("SetRemark failed: %v", err)
	}
	if a.Remark != "新备注名" {
		t.Errorf("expected remark updated to '新备注名', got %q", a.Remark)
	}

	// 读取落盘文件验证
	reloaded, err := ParseFile(filePath)
	if err != nil {
		t.Fatalf("reloading file failed: %v", err)
	}
	if reloaded.Remark != "新备注名" {
		t.Errorf("expected reloaded remark '新备注名', got %q", reloaded.Remark)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/auth -run TestAuthRemarkAndDisplayName`
Expected: FAIL due to undefined `Remark`, `DisplayName`, and `SetRemark`.

- [ ] **Step 3: Implement `Remark`, `saveAtomicLocked`, `SetRemark`, and `DisplayName` in `internal/auth/auth.go`**

In `internal/auth/auth.go`:
1. Add `Remark string` to `Auth` struct.
2. In `Parse()`:
   - Nested shape: add `Remark string json:"remark"` to `Account` struct.
   - Flat shape: add `Remark string json:"remark"` to `Flat` struct.
   - Assign `Remark: strings.TrimSpace(n.Account.Remark)` or `strings.TrimSpace(f.Remark)`.
3. In `SaveAtomic()`:
   - Extract `saveAtomicLocked() error` which holds `a.mu` and performs formatting and atomic write.
   - When writing `doc["account"]`:
     ```go
     acct := map[string]any{
         "uid":          a.UID,
         "enterpriseId": a.EnterpriseID,
         "nickname":     a.Nickname,
     }
     if a.Remark != "" {
         acct["remark"] = a.Remark
     }
     doc["account"] = acct
     ```
   - Make `SaveAtomic()` acquire `a.mu.Lock()` and call `a.saveAtomicLocked()`.
   - Implement `SetRemark(remark string) error`:
     ```go
     func (a *Auth) SetRemark(remark string) error {
         if a == nil {
             return fmt.Errorf("nil auth")
         }
         a.mu.Lock()
         defer a.mu.Unlock()
         a.Remark = strings.TrimSpace(remark)
         return a.saveAtomicLocked()
     }
     ```
4. Implement `DisplayName() string`:
   ```go
   func (a *Auth) DisplayName() string {
       if a == nil {
           return ""
       }
       a.mu.Lock()
       defer a.mu.Unlock()
       if a.Remark != "" {
           if a.Nickname != "" {
               return fmt.Sprintf("%s (%s)", a.Remark, a.Nickname)
           }
           if len(a.UID) > 8 {
               return fmt.Sprintf("%s (%s)", a.Remark, a.UID[:8])
           }
           return a.Remark
       }
       if a.Nickname != "" {
           return a.Nickname
       }
       if len(a.UID) > 8 {
           return a.UID[:8]
       }
       return a.UID
   }
   ```
5. In `internal/pool/entry.go`:
   - Add `Remark string json:"remark,omitempty"` and `DisplayName string json:"display_name,omitempty"` to `Status` struct.
6. In `internal/pool/state.go`:
   - In `statusOf(uid string, e *entry) Status`:
     ```go
     st.Remark = e.a.Remark
     st.DisplayName = e.a.DisplayName()
     ```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/auth -run TestAuthRemarkAndDisplayName && go test -v ./internal/pool`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/auth.go internal/auth/auth_test.go internal/pool/entry.go internal/pool/state.go
git commit -m "feat(auth): add Remark support, DisplayName helper, and thread-safe SetRemark"
```

---

### Task 2: 券码本地状态管理模块 (`internal/voucher`)

**Files:**
- Create: `internal/voucher/store.go`
- Create: `internal/voucher/store_test.go`

- [ ] **Step 1: Write the failing tests in `internal/voucher/store_test.go`**

```go
package voucher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func TestStorePersistenceAndStatus(t *testing.T) {
	tmpDir := t.TempDir()
	dataFile := filepath.Join(tmpDir, "vouchers.json")

	// 1. 初始化空 store
	s, err := NewStore(dataFile)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// 2. 模拟上游券码
	upstreamList := []upstream.SchoolVoucher{
		{Code: "V1001", GrantID: 1, PrizeName: "肯德基冰淇淋", ValidTo: "2026-10-24"},
		{Code: "V1002", GrantID: 2, PrizeName: "瑞幸咖啡券", ValidTo: "2026-10-25"},
	}

	// 首次检查：两张均为未通知
	unnotified := s.FilterUnnotified(upstreamList)
	if len(unnotified) != 2 {
		t.Fatalf("expected 2 unnotified, got %d", len(unnotified))
	}

	// 标记第一张为已通知
	if err := s.MarkNotified("uid-1", "测试号 (13800000000)", []upstream.SchoolVoucher{upstreamList[0]}); err != nil {
		t.Fatalf("MarkNotified failed: %v", err)
	}

	// 再次过滤：仅第二张未通知
	unnotified2 := s.FilterUnnotified(upstreamList)
	if len(unnotified2) != 1 || unnotified2[0].Code != "V1002" {
		t.Fatalf("expected only V1002 unnotified, got %+v", unnotified2)
	}

	// 标记 V1001 为已使用
	if err := s.SetUsed("V1001", true, "uid-1", "测试号", "肯德基冰淇淋", "2026-10-24"); err != nil {
		t.Fatalf("SetUsed failed: %v", err)
	}
	if !s.IsUsed("V1001") {
		t.Errorf("expected V1001 to be used")
	}

	// 3. 测试 Enrich
	enriched := s.Enrich(upstreamList)
	if len(enriched) != 2 {
		t.Fatalf("expected 2 enriched items")
	}
	if !enriched[0].IsUsed || enriched[0].UsedAt == nil {
		t.Errorf("expected enriched[0] to have IsUsed=true and non-nil UsedAt")
	}
	if enriched[1].IsUsed {
		t.Errorf("expected enriched[1] to have IsUsed=false")
	}

	// 4. 重启加载验证持久化
	s2, err := NewStore(dataFile)
	if err != nil {
		t.Fatalf("reload NewStore failed: %v", err)
	}
	if !s2.IsUsed("V1001") {
		t.Errorf("expected reloaded store to have V1001 used")
	}
	unnotifiedReload := s2.FilterUnnotified(upstreamList)
	if len(unnotifiedReload) != 1 || unnotifiedReload[0].Code != "V1002" {
		t.Errorf("expected reloaded store to preserve notified status")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/voucher`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement `Store` in `internal/voucher/store.go`**

Implement:
1. `VoucherRecord` struct:
   - `Code string json:"code"`
   - `GrantID int64 json:"grant_id,omitempty"`
   - `UID string json:"uid"`
   - `Nickname string json:"nickname"`
   - `PrizeName string json:"prize_name"`
   - `IsUsed bool json:"is_used"`
   - `UsedAt *time.Time json:"used_at,omitempty"`
   - `Notified bool json:"notified"`
   - `NotifiedAt *time.Time json:"notified_at,omitempty"`
   - `ValidTo string json:"valid_to,omitempty"`
2. `EnrichedVoucher` struct embedding or pairing `upstream.SchoolVoucher` with `IsUsed` and `UsedAt`.
3. `StoreData` struct with `Version int json:"version"` and `Vouchers map[string]*VoucherRecord json:"vouchers"`.
4. `Store` struct with `filePath string`, `mu sync.RWMutex`, `data StoreData`.
5. Methods:
   - `NewStore(filePath string) (*Store, error)`
   - `saveAtomicLocked() error` (using `.tmp` + `os.Rename`)
   - `IsUsed(code string) bool`
   - `SetUsed(code string, isUsed bool, uid, nickname, prizeName, validTo string) error`
   - `FilterUnnotified(vouchers []upstream.SchoolVoucher) []upstream.SchoolVoucher`
   - `MarkNotified(uid, nickname string, vouchers []upstream.SchoolVoucher) error`
   - `Enrich(vouchers []upstream.SchoolVoucher) []EnrichedVoucher`

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/voucher`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/voucher/
git commit -m "feat(voucher): implement voucher persistence store with atomic saves and status tracking"
```

---

### Task 3: Telegram 通知模块 (`internal/notify`)

**Files:**
- Create: `internal/notify/telegram.go`
- Create: `internal/notify/telegram_test.go`

- [ ] **Step 1: Write the failing tests in `internal/notify/telegram_test.go`**

```go
package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEscapeMarkdownV2(t *testing.T) {
	raw := "肯德基[冰淇淋]_*~`>#+-=|{}.!测试 (138)"
	escaped := escapeMarkdownV2(raw)
	// 验证特殊字符均被反斜杠转义
	for _, ch := range []string{"[", "]", "_", "*", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!", "(", ")"} {
		if !strings.Contains(escaped, "\\"+ch) {
			t.Errorf("expected character %q to be escaped with backslash, got %q", ch, escaped)
		}
	}
}

func TestSendVoucherWon(t *testing.T) {
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/bot123:TOKEN/sendMessage") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	n := NewNotifier(Config{
		Enabled:  true,
		BotToken: "123:TOKEN",
		ChatID:   "-1001234567",
	})
	n.apiBase = srv.URL // 覆盖为 mock 基础路径

	evt := VoucherWonEvent{
		DisplayName: "张三主号 (13800000000)",
		PrizeName:   "肯德基冰淇淋",
		SKUCode:     "kfc_ice_cream",
		Code:        "8247-9102-3841",
		ValidTo:     "2026-10-24",
		GrantedAt:   "2026-09-22T11:40:00+08:00",
	}

	err := n.SendVoucherWon(evt)
	if err != nil {
		t.Fatalf("SendVoucherWon failed: %v", err)
	}

	if receivedBody["chat_id"] != "-1001234567" {
		t.Errorf("chat_id mismatch: %v", receivedBody["chat_id"])
	}
	if receivedBody["parse_mode"] != "MarkdownV2" {
		t.Errorf("parse_mode must be MarkdownV2, got %v", receivedBody["parse_mode"])
	}
	text, _ := receivedBody["text"].(string)
	if !strings.Contains(text, "`8247-9102-3841`") {
		t.Errorf("expected code in backticks, got %s", text)
	}
}

func TestSendVoucherDisabled(t *testing.T) {
	n := NewNotifier(Config{Enabled: false})
	err := n.SendVoucherWon(VoucherWonEvent{Code: "123"})
	if err != nil {
		t.Fatalf("disabled notifier should return nil error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/notify`
Expected: FAIL with package not found.

- [ ] **Step 3: Implement `Telegram` notifier in `internal/notify/telegram.go`**

Implement:
1. `Config` struct: `Enabled bool`, `BotToken string`, `ChatID string`.
2. `Notifier` struct: `cfg Config`, `client *http.Client`, `apiBase string`.
3. `NewNotifier(cfg Config) *Notifier`: default `client` with 10s timeout, `apiBase: "https://api.telegram.org"`.
4. `Enabled() bool`.
5. `escapeMarkdownV2(s string) string`.
6. `SendVoucherWon(evt VoucherWonEvent) error`:
   - If `!n.cfg.Enabled || n.cfg.BotToken == "" || n.cfg.ChatID == ""`, return nil.
   - Formats MarkdownV2 message escaping `DisplayName` and `PrizeName`.
   - Sends POST to `<apiBase>/bot<token>/sendMessage`.
   - Checks HTTP status code (must be 2xx). If error or non-200, logs warning and returns error.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/notify`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notify/
git commit -m "feat(notify): implement Telegram Bot notifier with MarkdownV2 escaping"
```

---

### Task 4: 服务端配置与装配 (`cmd/server`)

**Files:**
- Modify: `cmd/server/config.go:18-35`
- Modify: `cmd/server/main.go:20-50,190-285,410-440`
- Modify: `config.example.json:1-10`

- [ ] **Step 1: Write test or verify existing config tests**

Run: `go test -v ./cmd/server`
Check how config is loaded and verified in `cmd/server/config_test.go`.

- [ ] **Step 2: Update `cmd/server/config.go`**

In `Config`:
```go
Telegram notify.Config `json:"telegram"`
VoucherFile string `json:"voucher_file"` // 缺省 ./data/vouchers.json
```
In default filling logic (`applyDefaults` / `Load`):
If `VoucherFile == ""`, default to `./data/vouchers.json`.

- [ ] **Step 3: Update `cmd/server/main.go`**

1. Instantiate:
   ```go
   vStore, err := voucher.NewStore(cfg.VoucherFile)
   if err != nil {
       log.Fatalf("初始化券码存储失败 (%s): %v", cfg.VoucherFile, err)
   }
   tgNotifier := notify.NewNotifier(cfg.Telegram)
   ```
2. Pass `VoucherStore: vStore, Notifier: tgNotifier` to `scheduler.Config`.
3. Pass `VoucherStore: vStore, Notifier: tgNotifier` to `panel.Config`.
4. In `restartRequiredFields()`:
   Add `"telegram"` and `"voucher_file"` so the panel UI advises restart upon configuration updates.
5. In `config.example.json`:
   Add:
   ```json
   "telegram": {
     "enabled": false,
     "bot_token": "",
     "chat_id": ""
   },
   "voucher_file": "./data/vouchers.json",
   ```

- [ ] **Step 4: Run server tests to verify it compiles and passes**

Run: `go test -v ./cmd/server`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/server/config.go cmd/server/main.go config.example.json
git commit -m "feat(server): wire telegram notifier and voucher store into server configuration"
```

---

### Task 5: 调度器抽奖后通知集成 (`internal/scheduler`)

**Files:**
- Modify: `internal/scheduler/scheduler.go:30-65`
- Modify: `internal/scheduler/school.go:50-85`

- [ ] **Step 1: Add `VoucherStore` and `Notifier` to `scheduler.Config` in `internal/scheduler/scheduler.go`**

In `internal/scheduler/scheduler.go`:
Add fields to `Config`:
```go
VoucherStore *voucher.Store
Notifier     *notify.Notifier
```

- [ ] **Step 2: Update `schoolAccount` in `internal/scheduler/school.go`**

In `internal/scheduler/school.go`:
1. In `schoolAccount(a *auth.Auth)`:
   After draw loop completes:
   ```go
   if chances > 0 && s.cfg.VoucherStore != nil {
       vouchers, err := s.cfg.Upstream.SchoolVouchers(a)
       if err == nil {
           s.checkAndNotifyVouchers(a, vouchers)
       } else {
           log.Printf("school %s: check vouchers failed: %v", logfmt.Label(a.UID, a.DisplayName()), err)
       }
   }
   ```
2. Implement `checkAndNotifyVouchers(a *auth.Auth, vouchers []upstream.SchoolVoucher)`:
   - Filter `unnotified := s.cfg.VoucherStore.FilterUnnotified(vouchers)`.
   - If empty, return.
   - If `s.cfg.Notifier == nil || !s.cfg.Notifier.Enabled()`:
     - Mark silent: `s.cfg.VoucherStore.MarkNotified(a.UID, a.DisplayName(), unnotified)`.
     - Return.
   - Send each unnotified voucher via `s.cfg.Notifier.SendVoucherWon`:
     ```go
     var successList []upstream.SchoolVoucher
     for _, v := range unnotified {
         evt := notify.VoucherWonEvent{
             DisplayName: a.DisplayName(),
             PrizeName:   v.PrizeName,
             SKUCode:     v.SKUCode,
             Code:        v.Code,
             ValidTo:     v.ValidTo,
             GrantedAt:   v.GrantedAt,
         }
         if err := s.cfg.Notifier.SendVoucherWon(evt); err != nil {
             log.Printf("school %s: tg notify voucher %s failed: %v", a.DisplayName(), v.Code, err)
         } else {
             successList = append(successList, v)
         }
     }
     if len(successList) > 0 {
         _ = s.cfg.VoucherStore.MarkNotified(a.UID, a.DisplayName(), successList)
     }
     ```

- [ ] **Step 3: Run scheduler tests**

Run: `go test -v ./internal/scheduler`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/scheduler/scheduler.go internal/scheduler/school.go
git commit -m "feat(scheduler): integrate incremental voucher check and telegram notification after school draw"
```

---

### Task 6: 面板 API 端点集成（账号备注、券码状态与增量通知）

**Files:**
- Modify: `internal/panel/panel.go:30-70,140-180`
- Modify: `internal/panel/taskcenter.go:570-620`
- Create: `internal/panel/voucher_api_test.go`

- [ ] **Step 1: Write the failing tests in `internal/panel/voucher_api_test.go`**

```go
package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/voucher"
)

func TestAccountRemarkAndVoucherStatusAPI(t *testing.T) {
	tmpDir := t.TempDir()
	authFile := filepath.Join(tmpDir, "workbuddy-u1.json")
	authJSON := `{"account":{"uid":"u1","nickname":"13800000000"},"auth":{"accessToken":"at","refreshToken":"rt","domain":"www.codebuddy.cn","realm":"cn"}}`
	_ = os.WriteFile(authFile, []byte(authJSON), 0o600)
	a, _ := auth.ParseFile(authFile)

	p := pool.New(pool.Config{})
	p.Add(a)

	vStore, _ := voucher.NewStore(filepath.Join(tmpDir, "vouchers.json"))

	pn := New(Config{
		Pool:         p,
		VoucherStore: vStore,
		APIKey:       "testkey",
	})

	// 1. 测试设置账号备注
	remarkReq := map[string]string{"remark": "测试备注1"}
	bodyBytes, _ := json.Marshal(remarkReq)
	req := httptest.NewRequest(http.MethodPost, "/panel/api/accounts/u1/remark", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer testkey")
	w := httptest.NewRecorder()
	pn.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if a.Remark != "测试备注1" {
		t.Errorf("expected remark '测试备注1', got %q", a.Remark)
	}

	// 2. 测试标记券码状态
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
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/panel -run TestAccountRemarkAndVoucherStatusAPI`
Expected: FAIL.

- [ ] **Step 3: Implement endpoints in `internal/panel/panel.go` and `internal/panel/taskcenter.go`**

1. In `panel.Config`: add `VoucherStore *voucher.Store` and `Notifier *notify.Notifier`.
2. In `Panel` struct: add `voucherStore`, `notifier`, and `voucherCheckMu sync.Mutex`.
3. In `registerRoutes()`:
   - Register `POST /panel/api/accounts/{uid}/remark`
   - Register `POST /panel/api/school/vouchers/status`
4. Implement `accountRemark(w http.ResponseWriter, r *http.Request)`:
   - Extract `uid := r.PathValue("uid")`.
   - Read JSON `{"remark": "..."}`.
   - Find auth via `p.cfg.Pool.AuthByUID(uid)`.
   - Call `a.SetRemark(body.Remark)`.
   - Return `{"ok": true, "uid": uid, "remark": a.Remark, "display_name": a.DisplayName()}`.
5. In `schoolVouchers(w http.ResponseWriter, r *http.Request)` in `internal/panel/taskcenter.go`:
   - When vouchers are loaded from upstream, call `p.cfg.VoucherStore.Enrich(vouchers)`.
   - Asynchronously trigger `go p.checkAndNotifyVouchers(a, vouchers)` with `voucherCheckMu` lock to deduplicate concurrent requests.
6. Implement `voucherStatus(w http.ResponseWriter, r *http.Request)`:
   - Extract `code`, `is_used`, `uid`, `prize_name`, `valid_to`.
   - Call `p.cfg.VoucherStore.SetUsed(...)`.
   - Return `{"ok": true, "code": body.Code, "is_used": body.IsUsed}`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/panel -run TestAccountRemarkAndVoucherStatusAPI`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/
git commit -m "feat(panel): add account remark and voucher status endpoints with enrichment"
```

---

### Task 7: 前端 Web 面板改造（「我的券码」隐藏切换/卡片操作 + 账号备注）

**Files:**
- Modify: `internal/panel/index.html:848-865`
- Modify: `internal/panel/app.js:350-420,1010-1070`

- [ ] **Step 1: Update HTML in `internal/panel/index.html`**

In `#vcVeil` modal:
1. In header (or footer):
   ```html
   <label class="check-inline" style="margin-left:auto;font-size:12px;cursor:pointer">
     <input type="checkbox" id="chkHideUsed"> 隐藏已使用
   </label>
   ```
2. Verify visual styling.

- [ ] **Step 2: Update JavaScript in `internal/panel/app.js`**

1. **券码卡片渲染 (`vcCard`)**：
   - Support `v.is_used`:
     - If `v.is_used` is true:
       - Card class includes `used` (CSS: opacity: 0.65).
       - Badge is `<span class="tag muted">已使用</span>`.
       - Button is `<button class="xs ghost" data-toggle-used="CODE" data-next="false">恢复未使用</button>`.
     - Else:
       - Badge is `<span class="tag ok">可使用</span>` or `<span class="tag bad">已过期</span>`.
       - Button is `<button class="xs ghost" data-toggle-used="CODE" data-next="true">标记已使用</button>`.
   - On button click:
     - Call `api('school/vouchers/status', { method: 'POST', body: { code, is_used: next } })`.
     - In-place update card status and DOM classes, update `v.is_used = next`.
     - Update `vcNote` summary counts.
2. **隐藏已使用切换 (`#chkHideUsed`)**：
   - When checkbox changes:
     - Toggle class `.hide-used` on `#vcBody` or toggle display of `.vc.used`.
     - Save preference to `localStorage.setItem('wb2api_hide_used_vouchers', checked)`.
3. **账号列表文字备注**：
   - In account rendering, display remark badge `🏷️ [remark]` next to nickname/phone.
   - If no remark, display clickable `+备注`.
   - Clicking remark or `+备注` triggers prompt: `输入账号备注 (如: 张三主号):`.
   - On input submit:
     - Call `api('accounts/' + uid + '/remark', { method: 'POST', body: { remark: newRemark } })`.
     - Update local account state and re-render row, toast `备注已更新`.

- [ ] **Step 3: Syntax and lint check on JS/HTML**

Run: `node -c internal/panel/app.js` (or node syntax verification).
Expected: No syntax errors.

- [ ] **Step 4: Commit**

```bash
git add internal/panel/index.html internal/panel/app.js
git commit -m "feat(ui): add hide-used toggle, mark-as-used actions on vouchers, and account remark editing"
```

---

### Task 8: 全量测试、构建验证与端到端回归

**Files:**
- Test all packages: `go test -v ./...`
- Test build: `go build -v -o /tmp/wb2api ./cmd/server`

- [ ] **Step 1: Run all unit and integration tests**

Run: `go test -v ./...`
Expected: All package tests PASS.

- [ ] **Step 2: Build server binary**

Run: `go build -v -o /tmp/wb2api ./cmd/server && rm -f /tmp/wb2api`
Expected: Build succeeds with 0 exit code and no warnings.

- [ ] **Step 3: Commit final plan execution checkpoint if needed**

```bash
git status
```
