//go:build windows

package supervisor

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// detachedAttr 让子进程不弹出控制台窗口。
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// terminateProcess 结束进程及其子进程。
//
// Windows 没有信号语义，taskkill 是等价的标准做法。
func terminateProcess(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T").Run()
}

// killProcess 强制结束进程树。
func killProcess(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}

// processAlive 判断进程是否仍存在。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}

// findFrpcProcesses 找出正在运行的 frpc 进程。
func findFrpcProcesses() []int {
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq frpc.exe", "/NH", "/FO", "CSV").Output()
	if err != nil {
		return nil
	}
	var pids []int
	self := os.Getpid()
	for _, line := range strings.Split(string(out), "\n") {
		cols := strings.Split(line, ",")
		if len(cols) < 2 {
			continue
		}
		pid, convErr := strconv.Atoi(strings.Trim(strings.TrimSpace(cols[1]), `"`))
		if convErr == nil && pid != self {
			pids = append(pids, pid)
		}
	}
	return pids
}

// unloadLegacyService 在 Windows 上没有需要摘除的旧版服务。
func unloadLegacyService() {}
