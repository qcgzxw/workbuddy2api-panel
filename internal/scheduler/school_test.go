package scheduler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/notify"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
	"github.com/linguo2625469/workbuddy2api-panel/internal/voucher"
)

type mockHTTPTransport struct {
	roundTrip func(req *http.Request) (*http.Response, error)
}

func (m *mockHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTrip(req)
}

func TestCheckAndNotifyVouchers(t *testing.T) {
	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()

	t.Run("Case 1: When Telegram is disabled, unnotified vouchers are silently marked as notified via store.MarkNotified", func(t *testing.T) {
		var tgCallCount atomic.Int32
		http.DefaultTransport = &mockHTTPTransport{
			roundTrip: func(req *http.Request) (*http.Response, error) {
				tgCallCount.Add(1)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				}, nil
			},
		}

		storePath := filepath.Join(t.TempDir(), "vouchers.json")
		vStore, err := voucher.NewStore(storePath)
		if err != nil {
			t.Fatalf("failed to create voucher store: %v", err)
		}

		notifier := notify.NewNotifier(notify.Config{
			Enabled:  false, // Disabled
			BotToken: "test-token",
			ChatID:   "12345",
		})

		s := &Scheduler{
			cfg: Config{
				VoucherStore: vStore,
				Notifier:     notifier,
			},
		}

		a := &auth.Auth{
			UID:      "u100",
			Nickname: "测试用户",
			Remark:   "主账号",
		}

		vouchers := []upstream.SchoolVoucher{
			{
				GrantID:   1,
				Code:      "CODE-DIS-1",
				PrizeName: "肯德基冰淇淋",
				SKUCode:   "kfc_ice_cream",
				ValidTo:   "2026-10-24",
				GrantedAt: "2026-09-22T10:00:00Z",
			},
			{
				GrantID:   2,
				Code:      "CODE-DIS-2",
				PrizeName: "瑞幸咖啡券",
				SKUCode:   "voucher_luckin",
				ValidTo:   "2026-10-30",
				GrantedAt: "2026-09-22T10:05:00Z",
			},
		}

		// Initial check: vouchers are unnotified
		if un := vStore.FilterUnnotified(vouchers); len(un) != 2 {
			t.Fatalf("expected 2 unnotified vouchers, got %d", len(un))
		}

		s.checkAndNotifyVouchers(a, vouchers)

		// Telegram should NOT receive any calls
		if calls := tgCallCount.Load(); calls != 0 {
			t.Errorf("expected 0 Telegram calls when disabled, got %d", calls)
		}

		// Vouchers should be silently marked as notified in store
		unnotified := vStore.FilterUnnotified(vouchers)
		if len(unnotified) != 0 {
			t.Errorf("expected 0 unnotified vouchers after silent mark, got %d", len(unnotified))
		}
	})

	t.Run("Case 2: When Telegram is enabled, each unnotified voucher is sent via Notifier.SendVoucherWon; only successful are marked", func(t *testing.T) {
		var mu sync.Mutex
		var sentCodes []string
		http.DefaultTransport = &mockHTTPTransport{
			roundTrip: func(req *http.Request) (*http.Response, error) {
				bodyBytes, _ := io.ReadAll(req.Body)
				bodyStr := string(bodyBytes)

				mu.Lock()
				defer mu.Unlock()

				if strings.Contains(bodyStr, "CODE-SUCCESS") {
					sentCodes = append(sentCodes, "CODE-SUCCESS")
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
					}, nil
				}
				if strings.Contains(bodyStr, "CODE-FAIL") {
					sentCodes = append(sentCodes, "CODE-FAIL")
					return &http.Response{
						StatusCode: http.StatusInternalServerError,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{"ok":false,"description":"internal server error"}`)),
					}, nil
				}
				return nil, errors.New("unexpected request")
			},
		}

		storePath := filepath.Join(t.TempDir(), "vouchers.json")
		vStore, err := voucher.NewStore(storePath)
		if err != nil {
			t.Fatalf("failed to create voucher store: %v", err)
		}

		notifier := notify.NewNotifier(notify.Config{
			Enabled:  true,
			BotToken: "test-token",
			ChatID:   "12345",
		})

		s := &Scheduler{
			cfg: Config{
				VoucherStore: vStore,
				Notifier:     notifier,
			},
		}

		a := &auth.Auth{
			UID:      "u200",
			Nickname: "小号2",
		}

		vouchers := []upstream.SchoolVoucher{
			{
				GrantID:   10,
				Code:      "CODE-SUCCESS",
				PrizeName: "肯德基冰淇淋",
				SKUCode:   "kfc_ice_cream",
				ValidTo:   "2026-10-24",
				GrantedAt: "2026-09-22T10:00:00Z",
			},
			{
				GrantID:   11,
				Code:      "CODE-FAIL",
				PrizeName: "酷狗音乐月卡",
				SKUCode:   "voucher_kugou",
				ValidTo:   "2026-11-01",
				GrantedAt: "2026-09-22T10:05:00Z",
			},
		}

		s.checkAndNotifyVouchers(a, vouchers)

		mu.Lock()
		calls := len(sentCodes)
		mu.Unlock()
		if calls != 2 {
			t.Fatalf("expected 2 Telegram attempts, got %d", calls)
		}

		// Filter unnotified: CODE-SUCCESS was marked, CODE-FAIL was NOT marked
		unnotified := vStore.FilterUnnotified(vouchers)
		if len(unnotified) != 1 {
			t.Fatalf("expected 1 unnotified voucher (the failed one), got %d", len(unnotified))
		}
		if unnotified[0].Code != "CODE-FAIL" {
			t.Errorf("expected remaining unnotified voucher to be CODE-FAIL, got %s", unnotified[0].Code)
		}
	})

	t.Run("Case 3: When all vouchers are already notified, no notification is sent and no store update occurs", func(t *testing.T) {
		var tgCallCount atomic.Int32
		http.DefaultTransport = &mockHTTPTransport{
			roundTrip: func(req *http.Request) (*http.Response, error) {
				tgCallCount.Add(1)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				}, nil
			},
		}

		storePath := filepath.Join(t.TempDir(), "vouchers.json")
		vStore, err := voucher.NewStore(storePath)
		if err != nil {
			t.Fatalf("failed to create voucher store: %v", err)
		}

		notifier := notify.NewNotifier(notify.Config{
			Enabled:  true,
			BotToken: "test-token",
			ChatID:   "12345",
		})

		s := &Scheduler{
			cfg: Config{
				VoucherStore: vStore,
				Notifier:     notifier,
			},
		}

		a := &auth.Auth{
			UID:      "u300",
			Nickname: "已通知用户",
		}

		vouchers := []upstream.SchoolVoucher{
			{
				GrantID:   20,
				Code:      "CODE-ALREADY",
				PrizeName: "肯德基冰淇淋",
				SKUCode:   "kfc_ice_cream",
			},
		}

		// Pre-mark as notified
		if err := vStore.MarkNotified(a.UID, a.DisplayName(), vouchers); err != nil {
			t.Fatalf("MarkNotified failed: %v", err)
		}

		// Execute checkAndNotifyVouchers
		s.checkAndNotifyVouchers(a, vouchers)

		if calls := tgCallCount.Load(); calls != 0 {
			t.Errorf("expected 0 Telegram calls when all already notified, got %d", calls)
		}
	})

	t.Run("Edge Cases: nil store, empty vouchers, nil notifier", func(t *testing.T) {
		a := &auth.Auth{UID: "u400", Nickname: "边缘测试"}
		vouchers := []upstream.SchoolVoucher{{Code: "EDGE-1"}}

		// Nil store -> no-op
		sNilStore := &Scheduler{cfg: Config{}}
		sNilStore.checkAndNotifyVouchers(a, vouchers)

		// Empty vouchers -> no-op
		storePath := filepath.Join(t.TempDir(), "vouchers.json")
		vStore, _ := voucher.NewStore(storePath)
		sEmpty := &Scheduler{cfg: Config{VoucherStore: vStore}}
		sEmpty.checkAndNotifyVouchers(a, nil)

		// Nil notifier -> silent mark
		sNilNotifier := &Scheduler{cfg: Config{VoucherStore: vStore, Notifier: nil}}
		sNilNotifier.checkAndNotifyVouchers(a, vouchers)
		if len(vStore.FilterUnnotified(vouchers)) != 0 {
			t.Errorf("expected silent mark when notifier is nil")
		}
	})
}

func TestSchoolAccountDrawAndCheckVouchers(t *testing.T) {
	oldDelay := schoolDrawDelay
	schoolDrawDelay = 0
	defer func() { schoolDrawDelay = oldDelay }()

	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()

	var tgMessages []string
	var tgMu sync.Mutex
	http.DefaultTransport = &mockHTTPTransport{
		roundTrip: func(req *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(req.Body)
			tgMu.Lock()
			tgMessages = append(tgMessages, string(b))
			tgMu.Unlock()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		},
	}

	var vouchersReqCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/portal/activity/school/tasks":
			// In period, tasks claimed so tasks step finishes fast
			resp := map[string]any{
				"code": 0,
				"msg":  "OK",
				"data": map[string]any{
					"in_period": true,
					"tasks": []map[string]any{
						{"task_code": "share_invite", "status": "claimed", "progress": 1, "target_count": 1},
						{"task_code": "desktop_chat_1_time", "status": "claimed", "progress": 1, "target_count": 1},
						{"task_code": "chat_3_times", "status": "claimed", "progress": 3, "target_count": 3},
						{"task_code": "expert_use", "status": "claimed", "progress": 1, "target_count": 1},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		case "/portal/activity/school/config":
			json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "OK",
				"data": map[string]any{
					"chance": map[string]any{
						"balance": 1,
					},
				},
			})
		case "/portal/activity/school/wheel/draw":
			json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "OK",
				"data": map[string]any{
					"prize_name": "肯德基冰淇淋",
				},
			})
		case "/portal/activity/school/vouchers":
			vouchersReqCount.Add(1)
			json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "OK",
				"data": map[string]any{
					"items": []map[string]any{
						{
							"grant_id":   999,
							"sku_code":   "kfc_ice_cream",
							"prize_name": "肯德基冰淇淋",
							"code":       "KFC-DRAWN-1234",
							"valid_to":   "2026-10-24",
							"granted_at": "2026-09-22T12:00:00Z",
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	storePath := filepath.Join(t.TempDir(), "vouchers.json")
	vStore, err := voucher.NewStore(storePath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	notifier := notify.NewNotifier(notify.Config{
		Enabled:  true,
		BotToken: "bot-token",
		ChatID:   "my-chat-id",
	})

	up := &upstream.Client{
		HTTP:          server.Client(),
		BillingBaseCN: server.URL,
	}

	s := New(Config{
		Upstream:     up,
		VoucherStore: vStore,
		Notifier:     notifier,
	})

	a := &auth.Auth{
		UID:         "u-draw-test",
		Nickname:    "抽奖测试",
		AccessToken: "test-token",
	}

	s.schoolAccount(a)

	if vouchersReqCount.Load() != 1 {
		t.Errorf("expected 1 call to /vouchers, got %d", vouchersReqCount.Load())
	}

	tgMu.Lock()
	tgCount := len(tgMessages)
	tgMu.Unlock()
	if tgCount != 1 {
		t.Errorf("expected 1 Telegram notification sent, got %d", tgCount)
	}

	// Verify the voucher is now marked in vStore
	checkList := []upstream.SchoolVoucher{{Code: "KFC-DRAWN-1234"}}
	if unnotified := vStore.FilterUnnotified(checkList); len(unnotified) != 0 {
		t.Errorf("expected voucher KFC-DRAWN-1234 to be marked as notified, but was still unnotified")
	}
}
