package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestWriteConfigAtomicReplacesContent 原子写的基本契约：内容替换、0600、无 tmp 残留。
func TestWriteConfigAtomicReplacesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"api_key":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeConfigAtomic(path, []byte(`{"api_key":"new"}`)); err != nil {
		t.Fatalf("writeConfigAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"api_key":"new"}` {
		t.Errorf("content=%s want new", got)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("tmp 残留, stat err=%v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode=%v want 0600", st.Mode().Perm())
	}
}

// TestWriteConfigAtomicDirUnwritableIsActionable 目录不可写（Docker 身份不匹配的现场）：
// 报错必须能分辨成因并带可操作指引，同时保留 %w 包装供调用方 errors.Is 判定。
func TestWriteConfigAtomicDirUnwritableIsActionable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root 无视目录权限")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	err := writeConfigAtomic(filepath.Join(dir, "config.json"), []byte("{}"))
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("err=%v 必须包装 fs.ErrPermission", err)
	}
	if !strings.Contains(err.Error(), "Docker 权限排障") {
		t.Errorf("错误文案缺少可操作指引: %v", err)
	}
}

// TestConfigReplaceErrorClassifiesEBUSY EBUSY 只有一个已知成因：目标是被单独挂载的文件
// （Docker 单文件 bind mount）。文案必须点明成因与解法，别再让人猜。
func TestConfigReplaceErrorClassifiesEBUSY(t *testing.T) {
	err := configReplaceError("/app/config.json", &os.LinkError{
		Op: "rename", Old: "/app/config.json.tmp", New: "/app/config.json", Err: syscall.EBUSY,
	})
	if !errors.Is(err, syscall.EBUSY) {
		t.Errorf("err=%v 必须包装 syscall.EBUSY", err)
	}
	for _, want := range []string{"挂载点", "目录挂载"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("EBUSY 文案缺 %q: %v", want, err)
		}
	}
	// 其他 rename 失败（EXDEV 等）不得套用 EBUSY 文案，避免误导。
	other := configReplaceError("/x/config.json", &os.LinkError{Op: "rename", Err: syscall.EXDEV})
	if strings.Contains(other.Error(), "挂载点") {
		t.Errorf("非 EBUSY 错误不该出现 EBUSY 文案: %v", other)
	}
}

// TestReadConfigErrorKeepsNotExistWrapping main.go 依赖 errors.Is(err, fs.ErrNotExist)
// 决定"是否自动生成配置"——加提示文案时绝不能破坏这层包装。
func TestReadConfigErrorKeepsNotExistWrapping(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load 必须保留 fs.ErrNotExist 包装，got %v", err)
	}
}

// TestConfigPrecheckWarnsOnUnwritableDir 启动预检：目录不可写 → 给出 WARN 文案（不阻断启动）。
func TestConfigPrecheckWarnsOnUnwritableDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root 无视目录权限")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	warn := configPrecheck(filepath.Join(dir, "config.json"))
	if warn == "" {
		t.Fatal("目录不可写时必须给出 WARN")
	}
	if !strings.Contains(warn, "Docker 权限排障") {
		t.Errorf("WARN 缺少指引: %s", warn)
	}
}

// TestConfigPrecheckSilentOnNormalFile 正常形态（同一设备上的普通文件）不得误报。
func TestConfigPrecheckSilentOnNormalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if warn := configPrecheck(path); warn != "" {
		t.Errorf("正常布局不该有 WARN: %s", warn)
	}
}

// TestIsSeparateMountIgnoresSymlink 判定必须用 Lstat：跟随符号链接会把同目录的链接
// 误判成"独立挂载"，进而给出错误的 EBUSY 警告。用跨设备目标（/dev/shm，通常 tmpfs）构造最严苛负例。
func TestIsSeparateMountIgnoresSymlink(t *testing.T) {
	if _, err := os.Stat("/dev/shm"); err != nil {
		t.Skip("/dev/shm 不存在")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "config.json")
	if err := os.Symlink("/dev/shm", link); err != nil {
		t.Skipf("建符号链接失败: %v", err)
	}
	if isSeparateMount(dir, link) {
		t.Error("符号链接被误判为独立挂载点")
	}
}
