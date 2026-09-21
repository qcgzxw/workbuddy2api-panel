// configio.go 配置文件落盘：原子写 + 失败分流 + 启动期预检。
//
// 单独成文件的原因：Docker 部署下"保存配置"有两条独立失败路径，报错必须能分辨——
//   ① 目录不可写（EACCES）：tmp 文件落在 config.json 同级目录，镜像层 /app 属主固定时必踩；
//   ② 目标是挂载点（EBUSY）：单文件 bind mount 让 rename(2) 无法覆盖 config.json。
// 平台相关原语（st_dev 判定、目录属主文案）见 permcheck_{unix,other}.go。
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// writeConfigAtomic 以 tmp + rename 原子替换 path（0600）。失败时返回带指引的错误。
func writeConfigAtomic(path string, out []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return configWriteError(tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return configReplaceError(path, err)
	}
	return nil
}

// configWriteError 包装"写临时文件"失败：目录不可写是 Docker 部署最常见的现场
// （容器内身份与配置目录属主不匹配）。保留 %w 供调用方 errors.Is 判定。
func configWriteError(tmp string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		dir := filepath.Dir(tmp)
		return fmt.Errorf("write config: %w\n（%s 不可写：当前 uid=%d，目录属主 %s。"+
			"默认部署会按挂载目录属主自动适配身份；若在 compose 里固定了 user:，"+
			"请改设 PUID/PGID 或让目录属主与之匹配。见 README「Docker 权限排障」）",
			err, dir, os.Getuid(), dirOwnerString(dir))
	}
	return fmt.Errorf("write config: %w", err)
}

// configReplaceError 包装 rename 失败。EBUSY 只有一个已知成因：EXDEV/权限都不长这样。
func configReplaceError(path string, err error) error {
	if errors.Is(err, syscall.EBUSY) {
		return fmt.Errorf("replace config: %w\n（%s 是被单独挂载的文件——rename 无法覆盖挂载点。"+
			"请把配置放进目录挂载（如 ./data/config.json），见 README「升级说明」）", err, path)
	}
	return fmt.Errorf("replace config: %w", err)
}

// readConfigError 包装读配置失败：身份与文件属主不匹配时（0600 + 属主不同）表现为裸
// permission denied + 进程退出重启，比面板保存更早出现。必须保留 %w 包装。
func readConfigError(path string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("read config: %w\n（%s 不可读：当前 uid=%d，目录属主 %s。"+
			"容器内身份由 entrypoint.sh 解析；若在 compose 里固定了 user:，"+
			"请改设 PUID/PGID 或让目录属主与之匹配。见 README「Docker 权限排障」）",
			err, path, os.Getuid(), dirOwnerString(filepath.Dir(path)))
	}
	return fmt.Errorf("read config: %w", err)
}

// configPrecheck 启动期预检：返回非空即为应打印的 WARN 文案。
// 只告警、不阻断启动——存量旧布局的部署必须能起来，才谈得上按 README 迁移。
func configPrecheck(path string) string {
	dir := filepath.Dir(path)
	// ① 目录可写性：用探针文件真实建删（比 access(2) 更贴近实际写路径，且跨平台）。
	// 探针名固定、写完即删；进程被 kill 时最多留下一个 0 字节文件（在 data/ 里，已被 .gitignore 覆盖）。
	probe := filepath.Join(dir, ".wb2a-writecheck")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return fmt.Sprintf("配置目录 %s 不可写（当前 uid=%d，目录属主 %s）：面板保存会失败，见 README「Docker 权限排障」",
			dir, os.Getuid(), dirOwnerString(dir))
	}
	_ = os.Remove(probe)
	// ② 挂载形态：目标若是被单独挂载的文件，tmp+rename 必然 EBUSY。
	if isSeparateMount(dir, path) {
		return fmt.Sprintf("%s 是被单独挂载的文件，原子替换（tmp+rename）会失败（EBUSY）："+
			"请把配置改到目录挂载下（如 ./data/config.json），见 README「升级说明」", path)
	}
	return ""
}
