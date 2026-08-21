// Package paths 集中管理本机所有路径，避免各处硬编码。
//
// 程序与隧道数据统一放在 ~/.frp-ngrok/ 下（若本机仍有 ~/.frpanel/ 则沿用）：
//   - bin/、token、port、prefs.json — 面板自身
//   - frp/ — 档案、frpc 二进制、部署脚本导出
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	// BinaryName 是安装后的可执行文件名。
	BinaryName = "frp-ngrok"
	// LegacyBinaryName 是重命名前的安装名，升级时仍能找到旧文件。
	LegacyBinaryName = "frpanel"
	// DaemonLabel 是 launchd 服务标识。
	DaemonLabel = "com.frpngrok.daemon"
	// LegacyDaemonLabel 是重命名前的 launchd 标识，安装/卸载时一并摘掉。
	LegacyDaemonLabel = "com.frpanel.daemon"
	// DefaultPort 为控制台监听端口，被占用时会自动顺延。
	DefaultPort = 17890
)

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// DataDir 是隧道数据根目录，位于 <AppDir>/frp/。
func DataDir() string { return filepath.Join(AppDir(), "frp") }

// ProfilesDir 存放所有服务器档案。
func ProfilesDir() string { return filepath.Join(DataDir(), "profiles") }

// CurrentFile 记录当前启用的档案名。
func CurrentFile() string { return filepath.Join(ProfilesDir(), "current") }

// ProfileDir 返回指定档案的目录。
func ProfileDir(id string) string { return filepath.Join(ProfilesDir(), id) }

// ProfileMeta 返回档案的服务器信息文件。
func ProfileMeta(id string) string { return filepath.Join(ProfileDir(id), "meta.conf") }

// ProfileConf 返回档案的 frpc 配置文件。
func ProfileConf(id string) string { return filepath.Join(ProfileDir(id), "frpc.toml") }

// ProfileLog 返回档案的 frpc 日志文件。
func ProfileLog(id string) string { return filepath.Join(ProfileDir(id), "frpc.log") }

// FrpcBin 是 frpc 可执行文件位置。
func FrpcBin() string { return filepath.Join(DataDir(), "frpc"+exeSuffix()) }

// InstalledBin 是自安装后的面板可执行文件位置。
// 即使本机还留着旧的 frpanel 二进制，新版本也必须写到新文件名，
// 否则 make install 会覆盖正在跑的旧进程文件，却找不到新包入口。
func InstalledBin() string {
	return filepath.Join(AppDir(), "bin", BinaryName+exeSuffix())
}

// LegacyInstalledBin 是重命名前的安装路径，升级时用来结束旧进程。
func LegacyInstalledBin() string {
	return filepath.Join(AppDir(), "bin", LegacyBinaryName+exeSuffix())
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// AppDir 存放面板自身的程序与运行时状态。
func AppDir() string {
	next := filepath.Join(home(), ".frp-ngrok")
	if dirExists(next) {
		return next
	}
	legacy := filepath.Join(home(), ".frpanel")
	if dirExists(legacy) {
		return legacy
	}
	return next
}

// TokenFile 保存控制台访问令牌。
func TokenFile() string { return filepath.Join(AppDir(), "token") }

// PortFile 记录控制台实际监听的端口，供启动器读取。
func PortFile() string { return filepath.Join(AppDir(), "port") }

// DaemonLog 是面板服务自身的日志。
func DaemonLog() string { return filepath.Join(AppDir(), "daemon.log") }

// DaemonErrLog 是面板服务的标准错误日志。
func DaemonErrLog() string { return filepath.Join(AppDir(), "daemon.err.log") }

// PrefsFile 保存界面语言等控制台偏好。
func PrefsFile() string { return filepath.Join(AppDir(), "prefs.json") }

// LaunchAgent 是 launchd 开机自启配置路径。
func LaunchAgent() string {
	return filepath.Join(home(), "Library", "LaunchAgents", DaemonLabel+".plist")
}

// LegacyLaunchAgent 是重命名前的 launchd 配置路径。
func LegacyLaunchAgent() string {
	return filepath.Join(home(), "Library", "LaunchAgents", LegacyDaemonLabel+".plist")
}

// ServerScriptDir 存放导出的服务端部署脚本。
func ServerScriptDir() string { return filepath.Join(DataDir(), "server") }

// PluginsDir 存放插件自身的数据（与档案/隧道数据分开，卸载时行为独立）。
func PluginsDir() string { return filepath.Join(AppDir(), "plugins") }

// HotkeysFile 是「命令行工具快捷键」插件的配置文件。
func HotkeysFile() string { return filepath.Join(PluginsDir(), "hotkeys.json") }

// AccessLogDir 存放访问日志插件的配置与各隧道日志。
func AccessLogDir() string { return filepath.Join(PluginsDir(), "access-log") }

// AccessLogConfigFile 是访问日志插件的配置文件。
func AccessLogConfigFile() string { return filepath.Join(AccessLogDir(), "config.json") }

// AccessLogFile 是某条隧道的访问日志。
func AccessLogFile(profileID string, port int) string {
	return filepath.Join(AccessLogProfileDir(profileID), fmt.Sprintf("%d.log", port))
}

// AccessLogProfileDir 是某台服务器档案下全部隧道访问日志的目录。
func AccessLogProfileDir(profileID string) string {
	return filepath.Join(AccessLogDir(), profileID)
}

// PortSitesDir 存放「本地端口管理」插件的配置与各端口默认站点目录。
func PortSitesDir() string { return filepath.Join(PluginsDir(), "port-sites") }

// PortSitesConfigFile 是本地端口管理插件的配置文件。
func PortSitesConfigFile() string { return filepath.Join(PortSitesDir(), "config.json") }

// PortSiteRoot 是某个端口的默认站点根目录。
func PortSiteRoot(port int) string {
	return filepath.Join(PortSitesDir(), fmt.Sprintf("%d", port))
}

// HotkeyRunsDir 存放「直接运行」每次执行的完整输出，便于对照退出码与脚本原文。
func HotkeyRunsDir() string { return filepath.Join(AppDir(), "hotkey-runs") }

// EnsureDirs 创建运行所需的全部目录。
func EnsureDirs() error {
	for _, d := range []string{
		DataDir(),
		ProfilesDir(),
		AppDir(),
		filepath.Join(AppDir(), "bin"),
		ServerScriptDir(),
		PluginsDir(),
		HotkeyRunsDir(),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
