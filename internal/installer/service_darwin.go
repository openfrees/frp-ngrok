//go:build darwin

package installer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>serve</string>
    </array>
    <key>WorkingDirectory</key>
    <string>%s</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ThrottleInterval</key>
    <integer>5</integer>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`

func guiTarget() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func unloadLegacyDaemon() {
	_ = exec.Command("launchctl", "bootout", guiTarget()+"/"+paths.LegacyDaemonLabel).Run()
	_ = os.Remove(paths.LegacyLaunchAgent())
}

// AutostartEnabled 判断是否已注册开机自启。
func AutostartEnabled() bool {
	_, err := os.Stat(paths.LaunchAgent())
	return err == nil
}

// ServiceLoaded 判断 launchd 中是否已加载本服务。
func ServiceLoaded() bool {
	return exec.Command("launchctl", "print", guiTarget()+"/"+paths.DaemonLabel).Run() == nil
}

// writePlist 生成 launchd 配置。
func writePlist() error {
	if err := os.MkdirAll(filepath.Dir(paths.LaunchAgent()), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf(plistTemplate,
		paths.DaemonLabel,
		paths.InstalledBin(),
		paths.DataDir(),
		paths.DaemonLog(),
		paths.DaemonErrLog(),
	)
	return os.WriteFile(paths.LaunchAgent(), []byte(body), 0o644)
}

// EnableAutostart 注册开机自启，并确保服务已加载运行。
func EnableAutostart() error {
	if err := writePlist(); err != nil {
		return err
	}
	if !ServiceLoaded() {
		return bootstrap()
	}
	port := ReadPort()
	if DaemonAlive(port) && !DaemonOutdated(port) {
		return nil
	}
	if DaemonAlive(port) {
		// 进程还活着，但已经不是磁盘上那份程序（常见于改了二进制文件名）。
		// kickstart 只会按内存里的旧 ProgramArguments 再拉起一次。
		return bootstrap()
	}
	return kickstartOrBootstrap(false)
}

// DisableAutostart 取消开机自启。
//
// 只删除 plist 文件、不卸载已加载的任务：当前会话的隧道继续运行，
// 下次登录时不再自动拉起。
func DisableAutostart() error {
	err := os.Remove(paths.LaunchAgent())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// bootstrap 把服务交给 launchd 托管。
func bootstrap() error {
	if err := os.MkdirAll(paths.AppDir(), 0o755); err != nil {
		return err
	}
	unloadLegacyDaemon()
	// 旧版可能以独立进程（而非 launchd）占着端口，开机自启开关关掉时尤其常见。
	_ = killDetached()
	// 先摘掉可能残留的旧任务，避免 bootstrap 报 already loaded。
	_ = exec.Command("launchctl", "bootout", guiTarget()+"/"+paths.DaemonLabel).Run()
	time.Sleep(200 * time.Millisecond)

	bootstrapOut, bootstrapErr := exec.Command("launchctl", "bootstrap", guiTarget(), paths.LaunchAgent()).CombinedOutput()
	var loadOut []byte
	if bootstrapErr != nil {
		// 老系统没有 bootstrap 子命令，退回旧接口。
		var loadErr error
		loadOut, loadErr = exec.Command("launchctl", "load", "-w", paths.LaunchAgent()).CombinedOutput()
		if loadErr != nil {
			return fmt.Errorf("注册后台服务失败: bootstrap: %v (%s); load: %v (%s)",
				bootstrapErr, strings.TrimSpace(string(bootstrapOut)),
				loadErr, strings.TrimSpace(string(loadOut)))
		}
	}
	if out, err := exec.Command("launchctl", "enable", guiTarget()+"/"+paths.DaemonLabel).CombinedOutput(); err != nil {
		return fmt.Errorf("启用后台服务失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	if !ServiceLoaded() {
		return fmt.Errorf("注册后台服务失败: launchd 未加载任务；bootstrap: %v (%s); load: %s",
			bootstrapErr, strings.TrimSpace(string(bootstrapOut)), strings.TrimSpace(string(loadOut)))
	}
	return nil
}

// kickstartOrBootstrap 不能只相信 launchctl 的退出码：macOS 偶尔会返回成功，
// 却把任务留在未加载或未响应状态。复核失败时重建任务，交给调用方继续等 HTTP 就绪。
func kickstartOrBootstrap(force bool) error {
	args := []string{"kickstart"}
	if force {
		args = append(args, "-k")
	}
	args = append(args, guiTarget()+"/"+paths.DaemonLabel)
	out, kickErr := exec.Command("launchctl", args...).CombinedOutput()
	if kickErr == nil && ServiceLoaded() {
		port := ReadPort()
		if port == 0 || WaitDaemon(port, 3*time.Second) {
			return nil
		}
	}
	if err := bootstrap(); err != nil {
		return fmt.Errorf("重启后台服务失败: kickstart: %v (%s); %w",
			kickErr, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// StartService 确保后台服务处于运行状态。
//
// 已开启自启时走 launchd 托管；用户关闭了自启则以脱离终端的独立进程启动，
// 这样关掉终端窗口或启动器都不会带走隧道。
func StartService() error {
	if AutostartEnabled() {
		if ServiceLoaded() {
			return kickstartOrBootstrap(false)
		}
		return bootstrap()
	}
	return spawnDetached()
}

// RestartService 重启后台服务，用于换上新安装的程序版本。
func RestartService() error {
	if AutostartEnabled() || ServiceLoaded() {
		// 升级时 plist 的 ProgramArguments 可能已经换成新二进制名。
		// kickstart -k 只会按内存里的旧路径再拉起一次，必须 bootstrap 才读新 plist。
		return bootstrap()
	}
	_ = killDetached()
	time.Sleep(500 * time.Millisecond)
	return spawnDetached()
}

// StopService 停止后台服务（隧道随之停止）。
func StopService() error {
	if ServiceLoaded() {
		if err := exec.Command("launchctl", "bootout", guiTarget()+"/"+paths.DaemonLabel).Run(); err != nil {
			_ = exec.Command("launchctl", "unload", paths.LaunchAgent()).Run()
		}
	}
	return killDetached()
}

// spawnDetached 以独立会话启动服务进程，使其不随父终端退出。
func spawnDetached() error {
	logFile, err := os.OpenFile(paths.DaemonLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(paths.InstalledBin(), "serve")
	cmd.Dir = paths.DataDir()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动后台服务失败: %w", err)
	}
	// 不等待子进程，交由系统回收。
	go func() { _ = cmd.Wait() }()
	return nil
}

// killDetached 结束以独立会话启动的服务进程（含重命名前的旧二进制）。
func killDetached() error {
	self := os.Getpid()
	seen := make(map[int]struct{})
	for _, pattern := range serveProcessPatterns() {
		out, err := exec.Command("pgrep", "-f", pattern).Output()
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(out)) {
			pid, convErr := strconv.Atoi(f)
			if convErr != nil || pid <= 0 || pid == self {
				continue
			}
			if _, ok := seen[pid]; ok {
				continue
			}
			seen[pid] = struct{}{}
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	return nil
}

// Uninstall 卸载服务与本机文件，保留用户的隧道配置。
func Uninstall() error {
	unloadLegacyDaemon()
	_ = exec.Command("launchctl", "bootout", guiTarget()+"/"+paths.DaemonLabel).Run()
	_ = os.Remove(paths.LaunchAgent())
	_ = killDetached()
	_ = os.Remove(paths.InstalledBin())
	_ = os.Remove(paths.LegacyInstalledBin())
	_ = os.Remove(paths.PortFile())
	return nil
}

// OpenBrowser 用系统默认浏览器打开控制台。
func OpenBrowser(url string) error {
	return exec.Command("open", url).Start()
}
