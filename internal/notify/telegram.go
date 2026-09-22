package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config represents Telegram Bot notification settings.
type Config struct {
	Enabled  bool   `json:"enabled"`
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}

// VoucherWonEvent holds the information for a newly acquired lottery voucher.
type VoucherWonEvent struct {
	DisplayName string
	PrizeName   string
	SKUCode     string
	Code        string
	ValidTo     string
	GrantedAt   string
}

// Notifier sends notification messages to Telegram.
type Notifier struct {
	mu      sync.RWMutex
	cfg     Config
	client  *http.Client
	apiBase string
}

// NewNotifier creates a new Telegram Notifier.
func NewNotifier(cfg Config) *Notifier {
	return &Notifier{
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		apiBase: "https://api.telegram.org",
	}
}

// Config returns a copy of current configuration in a thread-safe manner.
func (n *Notifier) Config() Config {
	if n == nil {
		return Config{}
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.cfg
}

// Reconfigure updates notifier configuration in a thread-safe manner.
func (n *Notifier) Reconfigure(cfg Config) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.cfg = cfg
}

// SetAPIBase sets a custom Telegram API base URL (primarily for testing).
func (n *Notifier) SetAPIBase(base string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.apiBase = base
}

// Enabled reports whether Telegram notification is enabled and configured.
func (n *Notifier) Enabled() bool {
	if n == nil {
		return false
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.cfg.Enabled && n.cfg.BotToken != "" && n.cfg.ChatID != ""
}

// escapeMarkdownV2 escapes all Telegram MarkdownV2 reserved characters:
// '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!', '\'
func escapeMarkdownV2(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + 16)
	for _, r := range s {
		switch r {
		case '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!', '\\':
			sb.WriteByte('\\')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// escapeMarkdownCode escapes '\' and '`' within Telegram MarkdownV2 inline code and pre blocks.
func escapeMarkdownCode(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + 4)
	for _, r := range s {
		switch r {
		case '`', '\\':
			sb.WriteByte('\\')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

type telegramSendMessageReq struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

// SendVoucherWon sends a notification about a newly won lottery voucher.
func (n *Notifier) SendVoucherWon(evt VoucherWonEvent) error {
	if !n.Enabled() {
		return nil
	}

	cfg := n.Config()

	validTo := evt.ValidTo
	if validTo == "" {
		validTo = "长期有效"
	}
	validTo = escapeMarkdownV2(validTo)

	grantedAt := evt.GrantedAt
	if grantedAt == "" {
		grantedAt = time.Now().Format("2006-01-02 15:04")
	}
	grantedAt = escapeMarkdownV2(grantedAt)

	code := evt.Code
	if code == "" {
		code = "-"
	}
	code = escapeMarkdownCode(code)

	text := fmt.Sprintf(
		"🎉 *WorkBuddy 抽奖中奖提醒*\n\n🎁 *奖品*：%s\n👤 *账号*：%s\n🎟️ *券码*：`%s`\n⏳ *有效期*：%s\n📅 *时间*：%s",
		escapeMarkdownV2(evt.PrizeName),
		escapeMarkdownV2(evt.DisplayName),
		code,
		validTo,
		grantedAt,
	)

	return n.sendRaw(cfg.BotToken, cfg.ChatID, text)
}

// SendTestMessage sends a test notification with given credentials, or notifier's current config if omitted.
func (n *Notifier) SendTestMessage(botToken, chatID string) error {
	if n == nil {
		return fmt.Errorf("telegram notifier not initialized")
	}
	cur := n.Config()
	if botToken == "" {
		botToken = cur.BotToken
	}
	if chatID == "" {
		chatID = cur.ChatID
	}
	botToken = strings.TrimSpace(botToken)
	chatID = strings.TrimSpace(chatID)
	if botToken == "" || chatID == "" {
		return fmt.Errorf("bot_token and chat_id are required")
	}

	text := fmt.Sprintf(
		"🎉 *WorkBuddy 抽奖中奖提醒（测试推送）*\n\n这是一条测试消息，验证您的 Telegram Bot 与 Chat ID 配置正常。\n当系统自动抽奖中奖时，券码将通过此通知渠道实时推送。\n\n📅 *测试时间*：%s",
		escapeMarkdownV2(time.Now().Format("2006-01-02 15:04:05")),
	)
	return n.sendRaw(botToken, chatID, text)
}

func (n *Notifier) sendRaw(botToken, chatID, text string) error {
	payloadBytes, err := json.Marshal(telegramSendMessageReq{
		ChatID:    chatID,
		Text:      text,
		ParseMode: "MarkdownV2",
	})
	if err != nil {
		return fmt.Errorf("marshal telegram payload: %w", err)
	}

	apiBase := n.apiBase
	if apiBase == "" {
		apiBase = "https://api.telegram.org"
	}
	apiBase = strings.TrimRight(apiBase, "/")

	client := n.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", apiBase, botToken)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		errMsg := err.Error()
		if botToken != "" {
			errMsg = strings.ReplaceAll(errMsg, botToken, "***")
		}
		return fmt.Errorf("execute telegram request: %s", errMsg)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("read telegram response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("WARN: telegram sendMessage failed: status=%d body=%s", resp.StatusCode, string(respBody))
		return fmt.Errorf("telegram sendMessage status %d: %s", resp.StatusCode, string(respBody))
	}

	var tgResp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(respBody, &tgResp); err != nil {
		return fmt.Errorf("unmarshal telegram response: %w", err)
	}
	if !tgResp.OK {
		log.Printf("WARN: telegram sendMessage ok=false: %s", tgResp.Description)
		return fmt.Errorf("telegram sendMessage failed: %s", tgResp.Description)
	}

	return nil
}
