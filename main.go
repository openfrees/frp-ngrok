// frp-ngrok — 单文件本地服务，用浏览器管理 frp 内网穿透隧道。
//
// 双击运行即完成安装、注册开机自启、拉起后台服务并打开控制台页面。
// 关闭浏览器或启动器都不影响隧道，只有停止本地服务才会断开。
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/openfrees/frp-ngrok/internal/client"
	"github.com/openfrees/frp-ngrok/internal/frpcbin"
	"github.com/openfrees/frp-ngrok/internal/hotkey"
	"github.com/openfrees/frp-ngrok/internal/installer"
	"github.com/openfrees/frp-ngrok/internal/notify"
	"github.com/openfrees/frp-ngrok/internal/paths"
	"github.com/openfrees/frp-ngrok/internal/server"
	"github.com/openfrees/frp-ngrok/internal/store"
	"github.com/openfrees/frp-ngrok/internal/supervisor"
	"github.com/openfrees/frp-ngrok/internal/tray"
)

// Version 可在构建时通过 -ldflags "-X main.Version=x.y.z" 覆盖。
var Version = "1.0.0"

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "":
		runLauncher()
	case "serve":
		runDaemon()
	case "open":
		runOpen()
	case "tray":
		runTray()
	case "install":
		mustInstall()
	case "uninstall":
		runUninstall()
	case "status":
		runStatus()
	case "version", "-v", "--version":
		fmt.Printf("frp-ngrok %s\n", Version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Printf("%s: %s\n\n", store.T("unknown command", "未知命令"), cmd)
		usage()
		os.Exit(1)
	}
}

func tr(en, zh string) string { return store.T(en, zh) }

func usage() {
	fmt.Printf(`frp-ngrok %s — %s

  %s
  frp-ngrok serve       %s
  frp-ngrok open        %s
  frp-ngrok tray        %s
  frp-ngrok install     %s
  frp-ngrok uninstall   %s
  frp-ngrok status      %s

  %s: %s
`, Version,
		tr("manage frp tunnels in the browser", "用浏览器管理 frp 内网穿透"),
		tr("run with no args     install if needed, open the console, stay in the menu bar", "直接双击运行        安装（如需要）、打开控制台并驻留菜单栏"),
		tr("run as a background service (used by the OS, not by hand)", "以后台服务方式运行（由系统调用，一般不用手敲）"),
		tr("open the console", "打开控制台页面"),
		tr("show the menu bar icon", "驻留菜单栏图标"),
		tr("install only, do not open the page", "只安装，不打开页面"),
		tr("uninstall the service and binary (keeps tunnel config)", "卸载服务与程序（保留隧道配置）"),
		tr("print status", "查看当前状态"),
		tr("config dir", "配置目录"), paths.DataDir())
}

// ---------- 启动器 ----------

// runLauncher 是双击可执行文件时走的路径：装好、跑起来、打开页面。
func runLauncher() {
	if notify.HasTerminal() {
		fmt.Println()
		fmt.Println("  frp-ngrok")
		fmt.Println("  ────────────────────────────────")
	}

	if err := paths.EnsureDirs(); err != nil {
		exitErr("创建工作目录失败", err)
	}

	freshInstall := !installer.Installed()
	if freshInstall {
		notify.Info(tr("First run, installing…", "首次运行，正在安装到本机…"))
	}
	if err := installer.InstallSelf(); err != nil {
		exitErr("安装失败", err)
	}

	// frpc 是实际打隧道的程序，缺失时先补齐。
	if !frpcbin.Present() {
		if err := frpcbin.Ensure(notify.Info); err != nil {
			exitErr("准备 frpc 失败", err)
		}
	}

	port := installer.ReadPort()
	if port > 0 && installer.DaemonAlive(port) {
		newPort, err := restartIfOutdated(func() {
			notify.Info(tr("New version detected, restarting the local service…", "检测到新版本，正在重启后台服务…"))
		})
		if err != nil {
			exitErr("重启后台服务失败", err)
		}
		openConsole(newPort)
		stayInMenuBar()
		return
	}

	var err error
	switch {
	case freshInstall:
		// 隧道本就该在重启后自动恢复，首次安装默认开启自启。
		err = installer.EnableAutostart()
	case installer.AutostartEnabled():
		err = installer.EnableAutostart()
	default:
		// 用户主动关过自启，就只启动这一次，不擅自改回他的设置。
		err = installer.StartService()
	}
	if err != nil {
		exitErr("启动后台服务失败", err)
	}

	port = waitForDaemon(20 * time.Second)
	if port == 0 {
		notify.Error("服务启动超时", "请查看日志了解原因：\n"+paths.DaemonErrLog())
		os.Exit(1)
	}
	openConsole(port)
	stayInMenuBar()
}

// stayInMenuBar 把菜单栏交给已安装程序的独立进程，随后让 .app 启动器退出。
//
// Finder 再次双击同一应用时，macOS 默认只唤醒仍在运行的旧应用实例。若启动器
// 自己卡在 tray.Run 里，新包的安装逻辑便永远没有机会执行。独立的 tray 命令不
// 属于应用包，既能继续常驻，也不会挡住下一份包启动。
func stayInMenuBar() {
	if !tray.Supported() || !fromAppBundle() || tray.AlreadyRunning() {
		return
	}
	cmd := exec.Command(paths.InstalledBin(), "tray")
	if err := cmd.Start(); err != nil {
		notify.Error("启动菜单栏图标失败", err.Error())
		return
	}
	if err := cmd.Process.Release(); err != nil {
		notify.Error("驻留菜单栏图标失败", err.Error())
	}
}

// fromAppBundle 判断当前进程是否由 .app 应用包启动。
func fromAppBundle() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.Contains(exe, ".app/Contents/MacOS/")
}

func startTray(port int) {
	token, err := server.LoadOrCreateToken()
	if err != nil {
		notify.Error("读取访问令牌失败", err.Error())
		return
	}
	// 阻塞直到用户在菜单里选择退出。
	tray.Run(client.New(port, token))
}

// runTray 单独驻留菜单栏，要求后台服务已在运行。
func runTray() {
	if !tray.Supported() {
		fmt.Println("当前构建不包含菜单栏功能（需要 macOS 且开启 CGO 构建）。")
		os.Exit(1)
	}
	if tray.AlreadyRunning() {
		fmt.Println("菜单栏图标已经在运行了。")
		return
	}
	port := installer.ReadPort()
	if port == 0 || !installer.DaemonAlive(port) {
		fmt.Println("后台服务没在运行，请先双击程序启动。")
		os.Exit(1)
	}
	startTray(port)
}

// restartIfOutdated 让正在跑的服务换上刚装好的程序，返回它此刻的端口。
//
// 覆盖 ~/.frpanel/bin 下的文件动不了已经在内存里跑的进程。少了这一步，
// 装完新版本控制台发出去的还是旧页面，而 status 照样显示「运行中」——
// 一切看着都对，只有界面莫名其妙停在上一个版本，极难往「没重启」上想。
//
// 服务没在跑时什么也不做：拉起服务是双击启动器和 launchd 的事，
// 装一次程序不该顺手把用户主动停掉的服务又开起来。
// onRestart 在真要重启前调用，让命令行与图形界面各自用合适的方式说这句话。
func restartIfOutdated(onRestart func()) (int, error) {
	port := installer.ReadPort()
	if port == 0 || !installer.DaemonAlive(port) || !installer.DaemonOutdated(port) {
		return port, nil
	}
	if onRestart != nil {
		onRestart()
	}
	if err := installer.RestartService(); err != nil {
		return 0, err
	}
	port = waitForDaemon(20 * time.Second)
	if port == 0 {
		return 0, fmt.Errorf("服务重启超时，请查看日志了解原因：%s", paths.DaemonErrLog())
	}
	return port, nil
}

// waitForDaemon 等到已安装的当前二进制真正接管后台，再返回它的监听端口。
func waitForDaemon(timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p := installer.ReadPort(); p > 0 && installer.DaemonAlive(p) && !installer.DaemonOutdated(p) {
			return p
		}
		time.Sleep(300 * time.Millisecond)
	}
	return 0
}

func openConsole(port int) {
	token, err := server.LoadOrCreateToken()
	if err != nil {
		exitErr("读取访问令牌失败", err)
	}
	url := installer.ConsoleURL(port, token)

	if notify.HasTerminal() {
		fmt.Println()
		fmt.Printf("  控制台已就绪：http://127.0.0.1:%d\n", port)
		fmt.Println("  关掉这个窗口和浏览器都不会断开隧道。")
		fmt.Println()
	}

	if err := installer.OpenBrowser(url); err != nil {
		notify.Error("无法自动打开浏览器",
			fmt.Sprintf("请手动访问 http://127.0.0.1:%d", port))
	}
}

func runOpen() {
	port := installer.ReadPort()
	if port == 0 || !installer.DaemonAlive(port) {
		fmt.Println("后台服务没在运行，请直接双击程序启动。")
		os.Exit(1)
	}
	openConsole(port)
}

func mustInstall() {
	if err := paths.EnsureDirs(); err != nil {
		exitErr("创建工作目录失败", err)
	}
	if err := installer.InstallSelf(); err != nil {
		exitErr("安装失败", err)
	}
	if err := installer.EnableAutostart(); err != nil {
		exitErr("注册开机自启失败", err)
	}
	// 装完必须让在跑的服务真的换上新程序，否则「安装完成」这句话是假的：
	// 磁盘上是新版，用户打开的控制台还是旧版，而 status 显示一切正常。
	if _, err := restartIfOutdated(func() {
		fmt.Println("检测到程序已更新，正在重启后台服务…")
	}); err != nil {
		exitErr("重启后台服务失败", err)
	}
	fmt.Println("安装完成，已注册开机自启。")
}

func runUninstall() {
	if err := installer.Uninstall(); err != nil {
		exitErr("卸载失败", err)
	}
	fmt.Println("已卸载后台服务与程序。")
	fmt.Printf("隧道配置仍保留在 %s，确认不再需要可自行删除。\n", paths.DataDir())
}

func runStatus() {
	port := installer.ReadPort()
	alive := port > 0 && installer.DaemonAlive(port)

	fmt.Printf("后台服务  : %s\n", boolText(alive, fmt.Sprintf("运行中 (127.0.0.1:%d)", port), "未运行"))
	fmt.Printf("开机自启  : %s\n", boolText(installer.AutostartEnabled(), "已开启", "未开启"))
	fmt.Printf("frpc      : %s\n", orText(frpcbin.InstalledVersion(), "未安装"))

	p, err := store.ResolveCurrent()
	if err != nil {
		fmt.Println("当前服务器: 未配置")
		return
	}
	fmt.Printf("当前服务器: %s  %s\n", p.ServerIP, p.DisplayDomain())
	tunnels, _ := store.LoadTunnels(p)
	fmt.Printf("隧道数量  : %d\n", len(tunnels))
	for _, t := range tunnels {
		fmt.Printf("  %-6d -> %s\n", t.LocalPort, t.PublicURL(p))
	}
}

// ---------- 后台服务 ----------

// runDaemon 以常驻服务方式运行：提供控制台并监管 frpc。
func runDaemon() {
	// 全局快捷键的事件循环必须跑在初始主线程上，这里尽早钉住，
	// 越晚越可能被调度器迁到别的线程，快捷键就静默失灵。
	hotkey.LockMainThread()

	log.SetFlags(log.Ldate | log.Ltime)
	log.SetPrefix("[frp-ngrok] ")

	if err := paths.EnsureDirs(); err != nil {
		log.Fatalf("创建工作目录失败: %v", err)
	}

	// 清掉上一轮遗留或旧版脚本注册的 frpc，避免多个进程抢同一份配置。
	supervisor.CleanupStrays()

	port, err := pickPort()
	if err != nil {
		log.Fatalf("%v", err)
	}

	sup := supervisor.New()
	srv, err := server.New(sup, port, Version)
	if err != nil {
		log.Fatalf("初始化控制台失败: %v", err)
	}

	ln, err := srv.Listen()
	if err != nil {
		log.Fatalf("%v", err)
	}
	if err := installer.WritePort(port); err != nil {
		log.Printf("记录端口失败: %v", err)
	}
	log.Printf("控制台监听 127.0.0.1:%d", port)

	// 有档案就自动把隧道连起来，这是开机自启的意义所在。
	if p, err := store.ResolveCurrent(); err == nil {
		go func() {
			if err := sup.Start(p); err != nil {
				log.Printf("自动连接 %s 未成功: %v", p.ServerIP, err)
			} else {
				log.Printf("已连上 %s，隧道就绪", p.ServerIP)
			}
		}()
	} else {
		log.Printf("尚未配置服务器，等待在控制台中接入")
	}

	// 收到停止信号时先摘掉 frpc，避免留下孤儿进程继续占着隧道。
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		log.Printf("收到停止信号，正在断开隧道…")
		sup.Stop()
		_ = ln.Close()
		os.Exit(0)
	}()

	// HTTP 服务放进协程；主线程交给 hotkey.RunMainLoop 跑 Carbon 事件循环。
	// macOS 的全局快捷键事件由主线程的运行循环分发，缺了这一步按键不生效。
	go func() {
		if err := srv.Serve(ln); err != nil {
			log.Printf("控制台已停止: %v", err)
		}
	}()
	hotkey.RunMainLoop()
}

// pickPort 优先沿用上次的端口，保证用户收藏的地址长期有效。
func pickPort() (int, error) {
	if last := installer.ReadPort(); last > 0 {
		if p, err := installer.PickPort(last); err == nil && p == last {
			return last, nil
		}
	}
	return installer.PickPort(paths.DefaultPort)
}

// ---------- 输出helper ----------

func exitErr(msg string, err error) {
	notify.Error(msg, err.Error())
	os.Exit(1)
}

func boolText(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

func orText(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
