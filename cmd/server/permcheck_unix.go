//go:build !windows

// permcheck_unix.go 平台原语：目录属主文案 + "目标是否被单独挂载"判定。
// 判定依据：被 bind mount 的文件其 st_dev 与所在目录不同；目录挂载内的普通文件则相同。
package main

import (
	"fmt"
	"os"
	"syscall"
)

// dirOwnerString 返回 "uid:gid"，取不到时返回 "?"（不编造）。
func dirOwnerString(dir string) string {
	st, err := os.Stat(dir)
	if err != nil {
		return "?"
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return "?"
	}
	return fmt.Sprintf("%d:%d", sys.Uid, sys.Gid)
}

// isSeparateMount 报告 path 是否是与 dir 不同 st_dev 的独立挂载点。
// 用 Lstat 而非 Stat：符号链接落在同一目录里，跟随目标会误判成挂载。
func isSeparateMount(dir, path string) bool {
	ts, err := os.Lstat(path)
	if err != nil {
		return false
	}
	ds, err := os.Stat(dir)
	if err != nil {
		return false
	}
	tsys, ok1 := ts.Sys().(*syscall.Stat_t)
	dsys, ok2 := ds.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false
	}
	return tsys.Dev != dsys.Dev
}
