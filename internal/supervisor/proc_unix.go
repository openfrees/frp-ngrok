//go:build !windows

package supervisor

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

// detachedAttr 让子进程自成进程组，便于整组回收。
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcess 请求进程组温和退出。
func terminateProcess(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
		return nil
	}
	// 目标可能不是进程组组长，退回单进程信号。
	return syscall.Kill(pid, syscall.SIGTERM)
}

// killProcess 强制结束进程组。
func killProcess(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

// processAlive 判断进程是否仍存在。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// findFrpcProcesses 找出所有指向本数据目录的 frpc 进程。
func findFrpcProcesses() []int {
	out, err := exec.Command("pgrep", "-f", paths.FrpcBin()+" -c ").Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if pid, convErr := strconv.Atoi(f); convErr == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// unloadLegacyService 摘掉旧版「隧道管理.command」注册的 launchd 任务。
func unloadLegacyService() {
	if runtime.GOOS != "darwin" {
		return
	}
	const legacyLabel = "com.frp.frpc"
	target := "gui/" + strconv.Itoa(os.Getuid()) + "/" + legacyLabel
	_ = exec.Command("launchctl", "bootout", target).Run()
	_ = exec.Command("launchctl", "remove", legacyLabel).Run()
}
