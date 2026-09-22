package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
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

// Enabled reports whether Telegram notification is enabled and configured.
func (n *Notifier) Enabled() bool {
	return n != nil && n.cfg.Enabled && n.cfg.BotToken != "" && n.cfg.ChatID != ""
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

	payloadBytes, err := json.Marshal(telegramSendMessageReq{
		ChatID:    n.cfg.ChatID,
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

	url := fmt.Sprintf("%s/bot%s/sendMessage", apiBase, n.cfg.BotToken)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		errMsg := err.Error()
		if n.cfg.BotToken != "" {
			errMsg = strings.ReplaceAll(errMsg, n.cfg.BotToken, "***")
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
