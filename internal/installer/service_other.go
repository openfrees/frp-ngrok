//go:build !darwin

package installer

import (
	"fmt"
	"os/exec"
	"runtime"
)

var errUnsupported = fmt.Errorf("当前系统 %s 的后台服务支持尚未实现", runtime.GOOS)

// AutostartEnabled 在未适配平台上恒为 false。
func AutostartEnabled() bool { return false }

// ServiceLoaded 在未适配平台上恒为 false。
func ServiceLoaded() bool { return false }

// EnableAutostart 尚未适配当前平台。
func EnableAutostart() error { return errUnsupported }

// DisableAutostart 尚未适配当前平台。
func DisableAutostart() error { return errUnsupported }

// StartService 尚未适配当前平台。
func StartService() error { return errUnsupported }

// StopService 尚未适配当前平台。
func StopService() error { return errUnsupported }

// RestartService 尚未适配当前平台。
func RestartService() error { return errUnsupported }

// Uninstall 尚未适配当前平台。
func Uninstall() error { return errUnsupported }

// OpenBrowser 尽力用系统命令打开浏览器。
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
