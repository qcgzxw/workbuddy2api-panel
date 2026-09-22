package voucher

import (
	"os"
	"path/filepath"
	"sync"
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

	// 5. 撤销已使用
	if err := s2.SetUsed("V1001", false, "uid-1", "测试号", "肯德基冰淇淋", "2026-10-24"); err != nil {
		t.Fatalf("revert SetUsed failed: %v", err)
	}
	if s2.IsUsed("V1001") {
		t.Errorf("expected V1001 to be unused after revert")
	}
}

func TestStoreConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	dataFile := filepath.Join(tmpDir, "vouchers.json")
	s, err := NewStore(dataFile)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			code := string(rune('A' + idx))
			_ = s.SetUsed(code, true, "uid", "name", "prize", "2026-10-24")
			_ = s.IsUsed(code)
			_ = s.MarkNotified("uid", "name", []upstream.SchoolVoucher{{Code: code, PrizeName: "prize"}})
			_ = s.FilterUnnotified([]upstream.SchoolVoucher{{Code: code}})
			_ = s.Enrich([]upstream.SchoolVoucher{{Code: code}})
		}(i)
	}
	wg.Wait()
}

func TestNilStoreSafety(t *testing.T) {
	var s *Store
	if s.IsUsed("V1") {
		t.Errorf("expected false for nil store IsUsed")
	}
	if err := s.SetUsed("V1", true, "u", "n", "p", "v"); err == nil {
		t.Errorf("expected error for nil store SetUsed")
	}
	vouchers := []upstream.SchoolVoucher{{Code: "V1"}}
	if unnotified := s.FilterUnnotified(vouchers); len(unnotified) != 1 {
		t.Errorf("expected 1 unnotified for nil store")
	}
	if err := s.MarkNotified("u", "n", vouchers); err != nil {
		t.Errorf("expected nil error for nil store MarkNotified, got %v", err)
	}
	if enriched := s.Enrich(vouchers); len(enriched) != 1 || enriched[0].IsUsed {
		t.Errorf("unexpected enriched result for nil store")
	}
}

func TestStoreFileHandling(t *testing.T) {
	tmpDir := t.TempDir()

	// Nested dir creation
	nestedFile := filepath.Join(tmpDir, "nested", "sub", "vouchers.json")
	s, err := NewStore(nestedFile)
	if err != nil {
		t.Fatalf("failed to create store in nested path: %v", err)
	}
	if err := s.SetUsed("V999", true, "uid", "name", "prize", "valid"); err != nil {
		t.Fatalf("failed to save into nested path: %v", err)
	}

	// Empty file handling
	emptyFile := filepath.Join(tmpDir, "empty.json")
	if err := os.WriteFile(emptyFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	sEmpty, err := NewStore(emptyFile)
	if err != nil {
		t.Fatalf("failed to open empty file: %v", err)
	}
	if sEmpty.IsUsed("V1") {
		t.Errorf("expected false for empty store")
	}

	// Invalid json file handling
	badFile := filepath.Join(tmpDir, "bad.json")
	if err := os.WriteFile(badFile, []byte("{invalid-json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(badFile); err == nil {
		t.Errorf("expected error when loading invalid json file")
	}
}

func TestStoreValidationAndRollback(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. 空路径校验
	if _, err := NewStore("   "); err == nil {
		t.Errorf("expected error for empty file path")
	}

	dataFile := filepath.Join(tmpDir, "store.json")
	s, err := NewStore(dataFile)
	if err != nil {
		t.Fatal(err)
	}

	// 2. 空券码校验
	if err := s.SetUsed("  ", true, "u", "n", "p", "v"); err == nil {
		t.Errorf("expected error for empty voucher code")
	}

	// 3. 落盘失败回滚验证
	// 初始化一张正常券
	if err := s.SetUsed("V1", false, "u", "n", "p", "v"); err != nil {
		t.Fatal(err)
	}
	// 将文件路径改为指向一个非法目标（如已存在的目录），使 rename/write 报错
	s.filePath = tmpDir // 目录路径无法作为普通文件写入
	if err := s.SetUsed("V1", true, "u", "n", "p", "v"); err == nil {
		t.Errorf("expected write failure when filePath is a directory")
	}
	// 验证内存状态回滚为原有的 IsUsed=false
	if s.IsUsed("V1") {
		t.Errorf("expected V1 IsUsed to remain false after failed SetUsed")
	}

	// 测试 MarkNotified 回滚
	if err := s.MarkNotified("u", "n", []upstream.SchoolVoucher{{Code: "V2"}}); err == nil {
		t.Errorf("expected error for failed MarkNotified")
	}
	if s.data.Vouchers["V2"] != nil {
		t.Errorf("expected V2 to be deleted from map after failed MarkNotified")
	}
}
