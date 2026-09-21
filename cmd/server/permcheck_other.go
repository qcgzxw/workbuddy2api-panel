//go:build !unix

// permcheck_other.go 非 Unix 桩：单文件挂载/EBUSY 是 Linux 容器侧形态，本机直接运行
// （Windows 双击 exe）不存在。保留同名函数，让调用点无需构建标签。
// 标签取 !unix（与 permcheck_unix.go 互补）：这样 plan9 等无 syscall.Stat_t 的平台也走桩。
package main

// dirOwnerString Windows 无 Unix uid 语义，固定返回 "?"（仅用于提示文案拼接）。
func dirOwnerString(string) string { return "?" }

// isSeparateMount Windows 不做该判定。
func isSeparateMount(string, string) bool { return false }

// permHintSuffix 非 Unix 无 uid/属主语义，不给指引（避免输出 "uid=-1" 这类无意义文案）。
func permHintSuffix(string) string { return "" }
