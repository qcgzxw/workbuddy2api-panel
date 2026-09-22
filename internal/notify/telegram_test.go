package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEscapeMarkdownV2(t *testing.T) {
	raw := "肯德基[冰淇淋]_*~`>#+-=|{}.!\\测试 (138)"
	escaped := escapeMarkdownV2(raw)
	// 验证所有 MarkdownV2 保留字符均被反斜杠转义
	for _, ch := range []string{"[", "]", "_", "*", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!", "(", ")", "\\"} {
		if !strings.Contains(escaped, "\\"+ch) {
			t.Errorf("expected character %q to be escaped with backslash, got %q", ch, escaped)
		}
	}
}

func TestEscapeMarkdownCode(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal-code", "normal-code"},
		{"code`with`ticks", "code\\`with\\`ticks"},
		{"code\\with\\backslash", "code\\\\with\\\\backslash"},
		{"`mixed\\code`", "\\`mixed\\\\code\\`"},
	}
	for _, tc := range tests {
		got := escapeMarkdownCode(tc.input)
		if got != tc.expected {
			t.Errorf("escapeMarkdownCode(%q) = %q; want %q", tc.input, got, tc.expected)
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
		PrizeName:   "肯德基[冰淇淋]",
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
	if !strings.Contains(text, "肯德基\\[冰淇淋\\]") {
		t.Errorf("expected escaped prize name, got %s", text)
	}
}

func TestSendVoucherDisabled(t *testing.T) {
	n := NewNotifier(Config{Enabled: false})
	err := n.SendVoucherWon(VoucherWonEvent{Code: "123"})
	if err != nil {
		t.Fatalf("disabled notifier should return nil error, got %v", err)
	}
}

func TestSendVoucherHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"description":"Bad Request: can't parse entities"}`))
	}))
	defer srv.Close()

	n := NewNotifier(Config{
		Enabled:  true,
		BotToken: "test",
		ChatID:   "123",
	})
	n.apiBase = srv.URL

	err := n.SendVoucherWon(VoucherWonEvent{Code: "V1"})
	if err == nil {
		t.Errorf("expected error when server returns 400")
	}
}

func TestNotifierEnabled(t *testing.T) {
	var nilNotifier *Notifier
	if nilNotifier.Enabled() {
		t.Errorf("expected nil notifier to be disabled")
	}

	if NewNotifier(Config{Enabled: false, BotToken: "tok", ChatID: "123"}).Enabled() {
		t.Errorf("expected Enabled:false to be disabled")
	}

	if NewNotifier(Config{Enabled: true, BotToken: "", ChatID: "123"}).Enabled() {
		t.Errorf("expected empty BotToken to be disabled")
	}

	if NewNotifier(Config{Enabled: true, BotToken: "tok", ChatID: ""}).Enabled() {
		t.Errorf("expected empty ChatID to be disabled")
	}

	if !NewNotifier(Config{Enabled: true, BotToken: "tok", ChatID: "123"}).Enabled() {
		t.Errorf("expected valid config to be enabled")
	}
}

func TestSendVoucherDefaults(t *testing.T) {
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	n := NewNotifier(Config{
		Enabled:  true,
		BotToken: "tok",
		ChatID:   "cid",
	})
	n.apiBase = srv.URL

	// Empty ValidTo and GrantedAt
	evt := VoucherWonEvent{
		DisplayName: "李四",
		PrizeName:   "代金券",
		Code:        "DEF-999",
	}

	err := n.SendVoucherWon(evt)
	if err != nil {
		t.Fatalf("SendVoucherWon failed: %v", err)
	}

	text, _ := receivedBody["text"].(string)
	if !strings.Contains(text, "长期有效") {
		t.Errorf("expected '长期有效' for empty ValidTo, got %s", text)
	}
	if !strings.Contains(text, "`DEF-999`") {
		t.Errorf("expected `DEF-999` in backticks, got %s", text)
	}
}

func TestSendVoucherCodeEscapingAndFallback(t *testing.T) {
	var receivedText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		receivedText, _ = req["text"].(string)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	n := NewNotifier(Config{Enabled: true, BotToken: "tok", ChatID: "cid"})
	n.apiBase = srv.URL + "/" // tests trailing slash trimming as well

	// Test code with backtick and backslash
	err := n.SendVoucherWon(VoucherWonEvent{
		Code: "ABC`123\\XYZ",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(receivedText, "`ABC\\`123\\\\XYZ`") {
		t.Errorf("expected code to be escaped inside backticks, got %s", receivedText)
	}

	// Test empty code fallback to "-"
	err = n.SendVoucherWon(VoucherWonEvent{
		Code: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(receivedText, "`-`") {
		t.Errorf("expected empty code to fallback to `-`, got %s", receivedText)
	}
}

func TestSendVoucherStatusOKWithFalseOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":false,"description":"Forbidden: bot was blocked by the user"}`))
	}))
	defer srv.Close()

	n := NewNotifier(Config{Enabled: true, BotToken: "tok", ChatID: "cid"})
	n.apiBase = srv.URL

	err := n.SendVoucherWon(VoucherWonEvent{Code: "V1"})
	if err == nil {
		t.Fatalf("expected error when 200 OK has ok:false, got nil")
	}
	if !strings.Contains(err.Error(), "Forbidden: bot was blocked by the user") {
		t.Errorf("expected description in error, got %v", err)
	}
}

func TestSendVoucherStatusOKWithInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<html><body>502 Bad Gateway</body></html>`))
	}))
	defer srv.Close()

	n := NewNotifier(Config{Enabled: true, BotToken: "tok", ChatID: "cid"})
	n.apiBase = srv.URL

	err := n.SendVoucherWon(VoucherWonEvent{Code: "V1"})
	if err == nil {
		t.Fatalf("expected error when 200 OK returns non-JSON HTML body, got nil")
	}
	if !strings.Contains(err.Error(), "unmarshal telegram response") {
		t.Errorf("expected unmarshal error, got %v", err)
	}
}

func TestSendVoucherTokenMaskingOnNetworkFailure(t *testing.T) {
	secretToken := "secret-bot-token-xyz123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srvURL := srv.URL
	srv.Close() // Close immediately to ensure network failure

	n := NewNotifier(Config{
		Enabled:  true,
		BotToken: secretToken,
		ChatID:   "cid",
	})
	n.apiBase = srvURL

	err := n.SendVoucherWon(VoucherWonEvent{Code: "V1"})
	if err == nil {
		t.Fatalf("expected network failure error, got nil")
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Errorf("secret token leaked in error message: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Errorf("expected token to be replaced by '***' in error message: %v", err)
	}
}

func TestSendVoucherApiBaseTrailingSlash(t *testing.T) {
	var requestedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	n := NewNotifier(Config{Enabled: true, BotToken: "tok", ChatID: "cid"})
	n.apiBase = srv.URL + "///"

	err := n.SendVoucherWon(VoucherWonEvent{Code: "V1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestedPath != "/bottok/sendMessage" {
		t.Errorf("expected path /bottok/sendMessage, got %s", requestedPath)
	}
}
