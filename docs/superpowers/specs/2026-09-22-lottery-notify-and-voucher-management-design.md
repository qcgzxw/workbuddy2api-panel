# 抽奖中奖 Telegram 提醒、券码状态管理与账号文字备注 — 设计

- 状态：待实现（实现计划另出）
- 影响范围：`config.json` / `config.example.json`、新增 `internal/notify`、新增 `internal/voucher`、`internal/auth`、`internal/pool`、`internal/scheduler/school.go`、`internal/panel/taskcenter.go`、`internal/panel/app.js`、`internal/panel/index.html`
- 目标：
  1. 抽奖中奖时通过 Telegram Bot 及时推送中奖通知（包含格式化券码、奖品名、账号备注、到期时间）；
  2. 提供券码已使用标记功能（持久化到本地 `data/vouchers.json`，Web 面板支持标记/撤销标记及切换隐藏已使用）；
  3. 为手机账号增加文字备注功能（持久化在账号凭据，Web 面板与日志/推送统一展示并支持编辑）。

---

## 1. 背景与需求分析

### 1.1 现状与痛点
1. **抽奖券码无主动通知**：WorkBuddy 开学季等活动包含抽奖，可能抽中第三方消费券（肯德基冰淇淋、瑞幸咖啡等券码）。当前系统仅在后台自动抽奖或手动抽奖，抽中后默默记录在日志或需用户手动进入「我的券码」查看，缺乏及时通知手段，容易错过使用期限导致浪费。
2. **券码核销状态未追踪**：上游 `/vouchers` 接口仅提供第三方券码的只读展示（上游无法标记线下第三方已使用状态）。用户在线下或第三方 App 核销券码后，无法在面板标记「已使用」，导致过期券、已用券与可用券混杂在一起。
3. **多账号识别困难**：凭据池中含有大量手机号凭据，缺乏自定义文字备注（如“主号”、“备用1”、“家人”等），在抽奖中奖或日志运维时难以一眼辨识具体账号。

### 1.2 明确的目标与约束
- **通知渠道**：单向推送至 Telegram Bot（不占用本地入站端口，不启动 Telegram Bot 轮询监听，遵循安全与轻量原则）。
- **网络与代理**：直接使用系统默认 HTTP 传输层，原生继承宿主机/容器环境变量中的 `HTTP_PROXY` / `HTTPS_PROXY`，免去冗余代理配置。
- **触发机制**：综合触发（每日排程抽奖后立即检测推送 + 面板刷新/闭环时比对增量推送），绝不错过任何新中奖券码。
- **持久化方案**：在 `data/vouchers.json` 中保存券码状态与推送记录；在账号凭据 `account.remark` 中保存账号文字备注，全部使用原子写入（tmp + rename），确保掉电/重启安全。
- **UI 体验**：面板券码卡片支持「标记已使用 / 恢复未使用」切换；支持「隐藏已使用」过滤；账号列表支持快捷查看与修改备注。

---

## 2. 总体架构与数据流

```
+--------------------------------------------------------------------------------+
|                             WorkBuddy Upstream API                             |
|        - POST /wheel/draw (抽奖)         - GET /vouchers (券码列表只读)          |
+--------------------------------------------------------------------------------+
                                       ▲
                                       │ 拉取/抽奖
       +-------------------------------+-------------------------------+
       │                                                               │
+---------------+                                             +-----------------+
| Scheduler     |                                             | Web Panel       |
| (定时抽奖)    |                                             | (任务中心/券码) |
+---------------+                                             +-----------------+
       │ 抽奖后触发同步                                                │ 列表刷新/手动闭环
       ▼                                                               ▼
+-------------------------------------------------------------------------------+
|                       internal/voucher.Store (券码状态仓库)                   |
|  - 加载/持久化 data/vouchers.json (原子落盘 tmp + rename)                      |
|  - 比对 FilterUnnotified() 识别新券码                                          |
|  - 维护 is_used / used_at / notified 状态                                     |
+-------------------------------------------------------------------------------+
       │ 发现新券码                                                    ▲ 标记状态 API
       ▼                                                               │
+-------------------------------+                     +-------------------------+
| internal/notify.Telegram      |                     | Web UI (app.js)         |
|  - Markdown 模板格式化        |                     |  - [隐藏已使用] 开关    |
|  - 包含账号备注/券码/有效期   |                     |  - [标记/恢复已使用]    |
|  - POST api.telegram.org      |                     |  - 账号备注编辑弹窗     |
+-------------------------------+                     +-------------------------+
       │                                                               │
       ▼                                                               ▼
+-------------------------------+                     +-------------------------+
| Telegram App (用户手机/群组)  |                     | data/vouchers.json      |
| 收到推送直接复制券码          |                     | auths/*.json (remark)   |
+-------------------------------+                     +-------------------------+
```

---

## 3. 详细组件设计

### 3.1 配置规范 (`config.json` 与 `config.example.json`)
在配置根节点增加 `telegram` 与可选的 `voucher_file`：

```json
{
  "telegram": {
    "enabled": false,
    "bot_token": "",
    "chat_id": ""
  },
  "voucher_file": "./data/vouchers.json"
}
```

- **`telegram.enabled`** (`bool`)：开关控制。未配置或为 `false` 时，抽奖中奖不触发外部网络请求。
- **`telegram.bot_token`** (`string`)：Telegram Bot 的 Access Token。
- **`telegram.chat_id`** (`string`)：接收消息的 Chat ID（支持个人 ID 或频道/群组负数 ID）。
- **`voucher_file`** (`string`)：本地持久化文件路径，缺省回落为 `./data/vouchers.json`。

### 3.2 Telegram 通知模块 (`internal/notify`)
新建轻量模块 `internal/notify`：

1. **结构体定义**：
   ```go
   type Config struct {
       Enabled  bool   `json:"enabled"`
       BotToken string `json:"bot_token"`
       ChatID   string `json:"chat_id"`
   }

   type Notifier struct {
       cfg  Config
       http *http.Client
   }
   ```
2. **HTTP 客户端**：
   - 超时设定为 `10 * time.Second`；
   - 使用 Go 标准 `http.DefaultTransport`（自动识别并遵从标准 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` 环境变量）。
3. **消息格式化与发送**：
   ```go
   type VoucherWonEvent struct {
       DisplayName string // 如 "张三主号 (17871043632)" 或 UID
       PrizeName   string // 如 "肯德基冰淇淋"
       SKUCode     string // 如 "kfc_ice_cream"
       Code        string // 如 "8247-9102-3841"
       ValidTo     string // 如 "2026-10-24"
       GrantedAt   string // 如 "2026-09-22T11:40:00+08:00"
   }
   ```
   Telegram Markdown 模板设计：
   ```markdown
   🎉 *WorkBuddy 抽奖中奖提醒*

   🎁 *奖品*：%s
   👤 *账号*：%s
   🎟️ *券码*：`%s`
   ⏳ *有效期*：%s
   📅 *时间*：%s
   ```
   - 券码包裹在 `` ` `` 代码块内，在移动端和桌面端 Telegram 点击券码可一键复制。
   - 发送失败记录 `log.Printf("notify telegram: send voucher failed: %v", err)`，不向调用方抛出致命阻断错误。

### 3.3 券码本地状态管理模块 (`internal/voucher`)
新建 `internal/voucher/store.go` 管理 `data/vouchers.json`：

1. **持久化数据结构**：
   ```go
   type VoucherRecord struct {
       Code        string    `json:"code"`
       GrantID     int64     `json:"grant_id,omitempty"`
       UID         string    `json:"uid"`
       Nickname    string    `json:"nickname"`
       PrizeName   string    `json:"prize_name"`
       IsUsed      bool      `json:"is_used"`
       UsedAt      *time.Time `json:"used_at,omitempty"`
       Notified    bool      `json:"notified"`
       NotifiedAt  *time.Time `json:"notified_at,omitempty"`
       ValidTo     string    `json:"valid_to,omitempty"`
   }

   type StoreData struct {
       Version  int                       `json:"version"`
       Vouchers map[string]*VoucherRecord `json:"vouchers"` // 键为券码 Code
   }
   ```

2. **核心方法**：
   - `NewStore(filePath string) (*Store, error)`：启动时加载文件；若文件不存在则初始化空字典。
   - `IsUsed(code string) bool`：查询使用状态。
   - `SetUsed(code string, isUsed bool, meta *VoucherMeta) error`：设置券码使用状态，并自动更新 `UsedAt`（取消使用时设为 nil），加写锁并调用 `saveAtomicLocked()`。
   - `FilterUnnotified(vouchers []upstream.SchoolVoucher) []upstream.SchoolVoucher`：基于本地记录过滤出 `!record.Notified` 的新券。
   - `MarkNotified(uid string, nickname string, vouchers []upstream.SchoolVoucher) error`：更新对应券码为 `Notified = true` 并原子持久化。
   - `Enrich(vouchers []upstream.SchoolVoucher) []EnrichedVoucher`：给上游返回的券码注入 `is_used` 与 `used_at` 字段，方便 Panel API 直接返回给前端。

3. **原子安全写入 (`saveAtomicLocked`)**：
   - 写入 `<filePath>.tmp`；
   - 执行 `os.Rename(tmp, filePath)`；
   - 确保持久化过程断电不丢数据、不产生半文件。

### 3.4 手机账号文字备注 (`internal/auth` & `internal/pool`)
1. **`internal/auth/auth.go`**：
   - `Auth` 结构体新增 `Remark string`。
   - `Parse()` 解析逻辑：在 `probe["auth"]` 嵌套形及扁平形两种结构的 `account` 中解析 `remark`：
     ```go
     type Account struct {
         UID          string `json:"uid"`
         EnterpriseID string `json:"enterpriseId"`
         Nickname     string `json:"nickname"`
         Remark       string `json:"remark,omitempty"`
     }
     ```
   - `SaveAtomic()` 持久化逻辑：将 `a.Remark` 写回 `account.remark`。
   - 新增方法 `DisplayName() string`：
     - 若 `Remark != ""`，返回 `fmt.Sprintf("%s (%s)", a.Remark, a.Nickname)`（若 Nickname 为空则回落 UID 截断）；
     - 若 `Remark == ""`，返回 `a.Nickname`（若为空则回落 UID 截断）。
   - 新增方法 `SetRemark(remark string) error`：修改内存并加锁调用 `SaveAtomic()`。

2. **`internal/pool/entry.go` & `state.go`**：
   - `Status` 结构体新增 `Remark string json:"remark,omitempty"` 和 `DisplayName string json:"display_name"`。
   - `p.statusOf(...)` 填入 `Remark: e.a.Remark, DisplayName: e.a.DisplayName()`。

3. **面板 API**：
   - `POST /panel/api/accounts/{uid}/remark`：
     - 请求体：`{"remark": "常用测试号"}`
     - 校验账号是否存在，调用 `a.SetRemark(remark)`，写盘并更新 `pool.State`，返回 `{"ok": true, "remark": "..."}`。

### 3.5 业务触发与端点串联

1. **调度器开学季抽奖触发 ([scheduler/school.go](file:///home/owen/docker/workbuddy2api-panel/internal/scheduler/school.go))**：
   - 在 `schoolAccount(a *auth.Auth)` 中，抽奖循环完成后：
     ```go
     if chances > 0 && s.cfg.VoucherStore != nil && s.cfg.Notifier != nil {
         vouchers, err := s.cfg.Upstream.SchoolVouchers(a)
         if err == nil {
             s.checkAndNotifyVouchers(a, vouchers)
         }
     }
     ```
   - `checkAndNotifyVouchers(a, vouchers)`：
     - 调用 `unnotified := s.cfg.VoucherStore.FilterUnnotified(vouchers)`；
     - 若有新券码，逐张组装 `VoucherWonEvent` 调用 `s.cfg.Notifier.SendVoucherWon(...)`；
     - 发送成功或尝试完毕后，调用 `s.cfg.VoucherStore.MarkNotified(a.UID, a.DisplayName(), unnotified)` 保存。

2. **面板任务中心券码端点 ([panel/taskcenter.go](file:///home/owen/docker/workbuddy2api-panel/internal/panel/taskcenter.go))**：
   - **`GET /panel/api/school/vouchers`**：
     - 拉取各账号上游 `/vouchers` 后，通过 `VoucherStore.Enrich(...)` 附加 `is_used` 和 `used_at`；
     - 同时异步做一次增量检测与通知（`go p.checkAndNotifyVouchers(a, vouchers)`），确保外部中奖同步推送；
     - 返回带有 `is_used`、`used_at` 的券码数组。
   - **`POST /panel/api/school/vouchers/status`**（新增）：
     - 请求体：`{"code": "...", "is_used": true/false, "uid": "...", "prize_name": "..."}`；
     - 调用 `VoucherStore.SetUsed(...)`；
     - 返回 `{"ok": true, "code": "...", "is_used": bool}`。

### 3.6 前端交互设计 (`index.html` & `app.js`)

1. **「我的券码」弹窗**：
   - **控制栏**：弹窗头部/底部增加 `<label class="check-lbl"><input type="checkbox" id="chkHideUsed"> 隐藏已使用</label>`；
   - **卡片渲染**：
     - 若 `is_used` 为真：展示灰标 `<span class="tag muted">已使用</span>`，卡片类名增加 `used`（CSS 样式透明度降为 0.65）；
     - 操作区提供按钮：
       - `is_used == false` 时：`<button class="xs ghost" data-toggle-used="CODE" data-next="true">标记已使用</button>`；
       - `is_used == true` 时：`<button class="xs ghost" data-toggle-used="CODE" data-next="false">恢复未使用</button>`；
     - 点击按钮调用 API 后就地原地刷新该卡片 DOM，无需整体重新拉取；
   - **隐藏切换**：勾选 `chkHideUsed` 时，通过 CSS 选择器或列表过滤隐藏所有 `.vc.used` 卡片；
   - **汇总文案**：底部文案计算总张数、已使用张数、可用张数。

2. **账号列表文字备注**：
   - 在主账号列表渲染行中，昵称/手机号右侧显示备注。已有备注显示徽章 `🏷️ 张三`，无备注显示浅色 `+备注`；
   - 点击备注唤出输入框，用户输入后回车或确认，调用 `POST /panel/api/accounts/{uid}/remark` 并局部刷新。

---

## 4. 错误处理与容错保障

| 故障场景 | 影响范围 | 处理策略 |
|---|---|---|
| Telegram API 超时或网络不可达 | 通知发送 | 超时设为 10s；失败仅记录 warning 日志，绝不阻断排程任务；`notified` 标志不置位，下次巡检自动重试 |
| `vouchers.json` 文件损坏或不存在 | 券码状态 | 若文件不存在自动新建；若损坏记录 error 并安全隔离备份，保证系统启动正常 |
| 并发写 `vouchers.json` | 状态持久化 | `Store` 内部全部方法持 `sync.RWMutex`，写操作写入 `.tmp` 文件后 `os.Rename` 原子覆盖 |
| 外部或手机端先行核销券码 | 核销状态 | 上游接口无核销回调，系统本地状态供用户手动标记，与上游只读特性解耦 |
| 账号凭据并发写入 | 账号备注 | 复用 `auth.Auth.mu` 与 `SaveAtomic()` 现存加锁与原子重命名机制，绝无竞争 |

---

## 5. 测试策略

1. **单元测试 (`internal/voucher`)**：
   - 测试 `NewStore` 在文件存在/缺失时的行为；
   - 测试 `SetUsed` 标记/撤销标记状态与时间戳更新；
   - 测试 `FilterUnnotified` 与 `MarkNotified` 的过滤去重逻辑；
   - 测试并发 `SetUsed` 与原子落盘的线程安全性。
2. **单元测试 (`internal/notify`)**：
   - 使用 mock `httptest.Server` 测试 Telegram 消息请求构造、Markdown 转义与错误重试处理。
3. **单元测试 (`internal/auth`)**：
   - 测试包含 `remark` 的嵌套形与扁平形凭据解析与 `SaveAtomic` 写回。
   - 测试 `DisplayName()` 在各种 nickname/remark/uid 组合下的回退规则。
4. **端到端测试 (E2E)**：
   - 验证 Web 面板标记券码已使用后刷新持久保留；
   - 验证修改账号备注后持久化到 `auths/*.json`，并在面板各处同步生效；
   - 触发抽奖闭环，验证 Telegram 能否收到包含券码和备注的 Markdown 格式消息。
