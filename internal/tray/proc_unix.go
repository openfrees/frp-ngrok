//go:build !windows

package tray

import (
	"os"
	"syscall"
)

// processExists 判断进程是否存在。
func processExists(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
