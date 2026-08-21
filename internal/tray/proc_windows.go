//go:build windows

package tray

import (
	"os/exec"
	"strconv"
	"strings"
)

// processExists 判断进程是否存在。
func processExists(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}
