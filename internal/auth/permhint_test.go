package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSaveAtomicPermissionHint 无写权限目录下保存，错误应包含 Docker 权限指引（锚点与 README 小节名一致）。
func TestSaveAtomicPermissionHint(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("Windows 无 POSIX 权限语义 / root 无权限限制，跳过")
	}
	dir := t.TempDir()
	ro := filepath.Join(dir, "ro")
	os.MkdirAll(ro, 0o555) // 只读目录
	defer os.Chmod(ro, 0o755)
	a := &Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1,
		UID: "u1", FilePath: filepath.Join(ro, "workbuddy-u1.json")}
	err := a.SaveAtomic()
	if err == nil {
		t.Fatal("只读目录保存应失败")
	}
	if !strings.Contains(err.Error(), "Docker 权限排障") || !strings.Contains(err.Error(), "PUID/PGID") {
		t.Errorf("权限错误应含 Docker 指引（锚点与 README 小节名一致 + 可操作方向），实际: %v", err)
	}
}
