// Package tray 提供 macOS 菜单栏常驻图标：显示状态灯，随时点开控制台。
//
// 菜单栏只是控制台的一个客户端，和浏览器地位相同。
// 退出菜单栏不会影响后台服务，隧道照常运行。
package tray

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/openfrees/frp-ngrok/internal/apitypes"
	"github.com/openfrees/frp-ngrok/internal/paths"
	"github.com/openfrees/frp-ngrok/internal/store"
)

// assets 内嵌四种状态的菜单栏图标。
//
//go:embed assets/*.png
var assets embed.FS

// 轮询到的状态映射成图标与文案。
type view struct {
	icon    string
	label   string
	tooltip string
}

func viewOf(s apitypes.State) view {
	n := len(s.Tunnels)
	switch s.Client.State {
	case "running":
		return view{
			icon:    "running",
			label:   store.T(fmt.Sprintf("Tunnels running · %d", n), fmt.Sprintf("隧道运行中 · %d 条", n)),
			tooltip: store.T("Tunnels running", "隧道运行中"),
		}
	case "starting":
		return view{icon: "warn", label: store.T("Connecting to server…", "正在连接服务器…"), tooltip: store.T("Connecting", "正在连接")}
	case "login_failed":
		return view{icon: "bad", label: store.T("Login rejected", "登录被拒绝"), tooltip: store.T("Login rejected", "登录被拒绝")}
	case "unreachable":
		return view{icon: "bad", label: store.T("Server unreachable", "连不上服务器"), tooltip: store.T("Server unreachable", "连不上服务器")}
	case "crashed":
		return view{icon: "bad", label: store.T("Client crashed", "客户端异常"), tooltip: store.T("Client crashed", "客户端异常")}
	default:
		return view{icon: "off", label: store.T("Tunnels stopped", "隧道已停止"), tooltip: store.T("Tunnels stopped", "隧道已停止")}
	}
}

// menuCopy 是菜单栏各条目的当前语言文案。
// Language 固定中英并列，免得切错语言后找不到开关。
type menuCopy struct {
	Reading     string
	Open        string
	OpenTip     string
	ClickTunnel string
	Start       string
	Stop        string
	Reconnect   string
	Quit        string
	QuitTip     string
	Unreachable string
	Language    string
	English     string
	Chinese     string
	Plugins     string
	Hotkeys     string
	AccessLog   string
	PortSites   string
}

func currentMenuCopy() menuCopy {
	return menuCopy{
		Reading:     store.T("Reading status…", "正在读取状态…"),
		Open:        store.T("Open console", "打开控制台"),
		OpenTip:     store.T("Open the console in a browser", "在浏览器中打开控制台页面"),
		ClickTunnel: store.T("Click to open in browser", "点击在浏览器中打开"),
		Start:       store.T("Start tunnels", "启动隧道"),
		Stop:        store.T("Stop tunnels", "停止隧道"),
		Reconnect:   store.T("Reconnect", "重新连接"),
		Quit:        store.T("Quit menu bar icon", "退出菜单栏图标"),
		QuitTip:     store.T("Tunnels keep running in the background", "隧道会继续在后台运行"),
		Unreachable: store.T("Local service unreachable", "连不上本地服务"),
		Language:    "Language / 语言",
		English:     "English",
		Chinese:     "中文",
		Plugins:     store.T("Plugins", "插件"),
		Hotkeys:     store.T("Command hotkeys", "命令行工具快捷键"),
		AccessLog:   store.T("Access log", "访问日志"),
		PortSites:   store.T("Local port sites", "本地端口管理"),
	}
}

func iconBytes(name string) []byte {
	b, err := assets.ReadFile("assets/tray-" + name + ".png")
	if err != nil {
		return nil
	}
	return b
}

// serverLine 是菜单里显示的服务器摘要。
func serverLine(s apitypes.State) string {
	if s.Current == nil {
		return store.T("No server connected", "尚未接入服务器")
	}
	return s.Current.ServerIP + " · " + s.Current.DisplayDomain()
}

// tunnelLabel 把隧道渲染成一行菜单文案，状态用行首的彩色圆点表示。
func tunnelLabel(t apitypes.Tunnel) string {
	return fmt.Sprintf("%d  →  %s", t.LocalPort, strings.TrimSuffix(
		strings.TrimPrefix(t.URL, "https://"), "/"))
}

// tunnelDot 选择隧道行首的状态点。
//
// 绿色表示这个地址现在真能访问；红色表示隧道已就位但本机端口没服务，
// 访问只会拿到 frp 的 404；客户端整体停止时全部转灰。
func tunnelDot(t apitypes.Tunnel, clientRunning bool) []byte {
	switch {
	case !clientRunning:
		return dotBytes("off")
	case t.LocalUp:
		return dotBytes("ok")
	default:
		return dotBytes("bad")
	}
}

func dotBytes(name string) []byte {
	b, err := assets.ReadFile("assets/dot-" + name + ".png")
	if err != nil {
		return nil
	}
	return b
}

// ---------- 单实例控制 ----------

func pidFile() string { return filepath.Join(paths.AppDir(), "tray.pid") }

// AlreadyRunning 判断菜单栏图标是否已在运行，避免重复驻留。
func AlreadyRunning() bool {
	b, err := os.ReadFile(pidFile())
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return false
	}
	return processExists(pid)
}

func writePID() {
	_ = os.MkdirAll(paths.AppDir(), 0o755)
	_ = os.WriteFile(pidFile(), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

func removePID() { _ = os.Remove(pidFile()) }
