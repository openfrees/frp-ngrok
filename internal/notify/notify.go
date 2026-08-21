// Package notify 在没有终端窗口时（例如从访达双击 .app 启动）改用系统弹窗提示。
package notify

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// HasTerminal 判断标准输出是否连着终端。
func HasTerminal() bool {
	if os.Getenv("FRPANEL_FORCE_TERMINAL") == "1" {
		return true
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// Info 输出一条普通提示：有终端就打印，没有就发系统通知。
func Info(msg string) {
	if HasTerminal() {
		println("  " + msg)
		return
	}
	Notify("frp-ngrok", msg)
}

// Notify 始终弹出系统通知，不看有没有终端。
//
// 快捷键「直接运行」由后台 daemon 收尾：stdout 是日志文件，HasTerminal 为假也
// 好、为真也只是打到 daemon.log。结果必须让用户在通知中心看见。
func Notify(title, message string) {
	if runtime.GOOS != "darwin" {
		println("  " + title + ": " + message)
		return
	}
	_ = exec.Command("osascript", "-e", notificationScript(title, message)).Run()
}

func notificationScript(title, message string) string {
	return `display notification "` + escape(message) + `" with title "` + escape(title) + `"`
}

// Error 报告一条错误：无终端时弹出对话框，避免双击后毫无反馈。
func Error(title, detail string) {
	if HasTerminal() {
		println("\n  " + title + ": " + detail + "\n")
		return
	}
	if runtime.GOOS == "darwin" {
		script := `display alert "` + escape(title) + `" message "` + escape(detail) + `" as critical`
		_ = exec.Command("osascript", "-e", script).Run()
	}
}

// escape 转义 AppleScript 字符串字面量中的特殊字符。
func escape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ")
	return r.Replace(s)
}
