package hotkey

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/openfrees/frp-ngrok/internal/notify"
	"github.com/openfrees/frp-ngrok/internal/store"
)

// Execute 按条目的动作执行命令，供快捷键触发时调用。
//
// 「直接运行」在后台启动、不阻塞事件循环；打开终端类动作需要等 AppleScript
// 返回才能知道成功与否，因此是同步的，但都很快。
func Execute(item store.HotkeyItem) error {
	switch item.Action {
	case store.HotkeyActionRun:
		return runInShell(item.Name, item.Command)
	case store.HotkeyActionTerminal:
		return openTerminal(item.Command)
	case store.HotkeyActionITerm:
		return openITerm(item.Command)
	}
	return fmt.Errorf("不认识的执行方式: %s", item.Action)
}

// Test 从控制台试跑一条命令，返回给用户看的文字结果。
//
// 「直接运行」改成同步执行并带回输出，方便验证命令写得对不对；其余与 Execute 一致。
func Test(item store.HotkeyItem) (string, error) {
	switch item.Action {
	case store.HotkeyActionRun:
		return runInShellSync(item.Command)
	case store.HotkeyActionTerminal:
		if err := openTerminal(item.Command); err != nil {
			return "", err
		}
		return "已让「终端」打开并注入命令。", nil
	case store.HotkeyActionITerm:
		if err := openITerm(item.Command); err != nil {
			return "", err
		}
		return "已让 iTerm2 打开并注入命令。", nil
	}
	return "", fmt.Errorf("不认识的执行方式: %s", item.Action)
}

// loginShell 返回用户登录 shell，找不到时退回到系统默认。
func loginShell() string {
	if sh := strings.TrimSpace(os.Getenv("SHELL")); sh != "" {
		return sh
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat("/bin/zsh"); err == nil {
			return "/bin/zsh"
		}
		return "/bin/bash"
	}
	return "/bin/sh"
}

// runInShell 后台跑一条命令：不阻塞，并把输出写进右上角的状态窗口。
func runInShell(name, command string) error {
	if strings.TrimSpace(name) == "" {
		name = "直接运行"
	}
	statusID := openRunStatus(name, command)
	logFile, logPath, logErr := openHotkeyRunLog(name, command, time.Now())
	if logErr != nil {
		log.Printf("[快捷键] 无法写入运行日志: %v", logErr)
	}
	shell := loginShell()
	cmd := exec.Command(shell, shellArgs(shell, command)...)
	cmd.Env = shellEnv(os.Environ())
	writer := runStatusWriter{
		statusID: statusID,
		file:     logFile,
		feed:     attachRunStatusFeed(statusID),
	}
	terminal, err := attachCommandTerminal(cmd)
	if err != nil {
		reportRunFinished(name, statusID, logFile, logPath, fmt.Errorf("启动命令终端失败: %w", err))
		return fmt.Errorf("启动命令终端失败: %w", err)
	}
	unregisterInput := registerRunStatusInput(statusID, terminal.input)
	if err := cmd.Start(); err != nil {
		unregisterInput()
		terminal.close()
		reportRunFinished(name, statusID, logFile, logPath, fmt.Errorf("启动命令失败: %w", err))
		return fmt.Errorf("启动命令失败: %w", err)
	}
	terminal.afterStart()
	var copied sync.WaitGroup
	copied.Add(1)
	go func() {
		defer copied.Done()
		_, _ = io.Copy(writer, terminal.output)
	}()
	// 不收尸会留下僵尸进程；命令本身跑多久都行。
	go func() {
		defer unregisterInput()
		waitErr := cmd.Wait()
		terminal.close()
		copied.Wait()
		reportRunFinished(name, statusID, logFile, logPath, waitErr)
	}()
	return nil
}

func reportRunFinished(name string, statusID int, logFile *os.File, logPath string, runErr error) {
	ok := runErr == nil
	exitCode := -1
	if ok {
		exitCode = 0
	} else if exitErr, isExit := runErr.(*exec.ExitError); isExit {
		exitCode = exitErr.ExitCode()
	}
	if logFile != nil {
		_, _ = logFile.WriteString(formatRunLogFooter(ok, exitCode, runErr, time.Now()))
		_ = logFile.Close()
	}
	finishRunStatus(statusID, formatRunWindowFooter(ok, exitCode, logPath, runErr), ok)
	notify.Notify(hotkeyRunNotifyTitle, formatRunNotifyMessage(name, ok, exitCode))
	if ok {
		log.Printf("[快捷键] %q 执行成功 日志=%s", name, logPath)
		return
	}
	log.Printf("[快捷键] %q 执行失败: %v 日志=%s", name, runErr, logPath)
}

type commandTerminal struct {
	input      io.WriteCloser
	output     io.ReadCloser
	afterStart func()
	close      func()
}

type runStatusWriter struct {
	statusID int
	file     io.Writer
	feed     *runStatusFeed
}

func (w runStatusWriter) Write(p []byte) (int, error) {
	if w.file != nil {
		_, _ = w.file.Write(p)
	}
	if w.feed != nil {
		_, _ = w.feed.Write(p)
	}
	return len(p), nil
}

var runStatusInputs sync.Map

func registerRunStatusInput(id int, input io.Writer) func() {
	if id == 0 || input == nil {
		return func() {}
	}
	runStatusInputs.Store(id, input)
	return func() {
		runStatusInputs.Delete(id)
	}
}

func sendRunStatusInput(id int, text string) {
	if id == 0 || text == "" {
		return
	}
	input, ok := runStatusInputs.Load(id)
	if !ok {
		return
	}
	_, _ = io.WriteString(input.(io.Writer), text)
}

// runInShellSync 同步跑命令，最多等 12 秒，把输出带回给控制台验证。
func runInShellSync(command string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	shell := loginShell()
	cmd := exec.CommandContext(ctx, shell, shellArgs(shell, command)...)
	cmd.Env = shellEnv(os.Environ())
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return strings.TrimSpace(out.String()), fmt.Errorf("命令超过 12 秒未结束，已中止")
		}
		return strings.TrimSpace(out.String()), err
	}
	if s := strings.TrimSpace(out.String()); s != "" {
		return s, nil
	}
	return "命令已执行（无输出）。", nil
}

func shellArgs(shell, command string) []string {
	switch filepath.Base(shell) {
	case "zsh", "bash":
		return []string{"-l", "-i", "-c", command}
	default:
		return []string{"-l", "-c", command}
	}
}

func shellEnv(base []string) []string {
	env := append([]string(nil), base...)
	return append(env, "FRPANEL_FORCE_TERMINAL=1")
}

// openTerminal 打开系统自带「终端」：还没开就开新窗口，已经开了就在当前窗口
// 开一个新 tab 注入命令，不打扰别人正在跑的会话。
func openTerminal(command string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("打开「终端」仅在 macOS 上可用")
	}
	return runAppleScript(terminalScript(command))
}

func terminalScript(command string) string {
	escaped := applescriptString(command)
	return `tell application "System Events" to set terminalWasRunning to exists process "Terminal"
tell application "Terminal"
	if not terminalWasRunning then
		do script "` + escaped + `"
		activate
	else if (count of windows) = 0 then
		do script "` + escaped + `"
		activate
	else
		activate
		delay 0.1
		tell application "System Events" to keystroke "t" using command down
		delay 0.15
		do script "` + escaped + `" in selected tab of front window
	end if
end tell`
}

// openITerm 打开 iTerm2：还没开就开新窗口，已经开了就在当前窗口开一个新 tab
// 再注入命令；未安装时把错误如实带回。
func openITerm(command string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("打开 iTerm2 仅在 macOS 上可用")
	}
	script := `tell application "iTerm"
	activate
	if (count of windows) = 0 then
		create window with default profile
	else
		tell current window to create tab with default profile
	end if
	tell current session of current window
		write text "` + applescriptString(command) + `"
	end tell
end tell`
	return runAppleScript(script)
}

func runAppleScript(script string) error {
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// applescriptString 把命令安全地塞进 AppleScript 字符串字面量。
func applescriptString(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ", "\r", " ").Replace(s)
}
