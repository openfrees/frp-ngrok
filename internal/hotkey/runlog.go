package hotkey

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

const hotkeyRunNotifyTitle = "frp-ngrok"

func openHotkeyRunLog(name, command string, now time.Time) (*os.File, string, error) {
	dir := paths.HotkeyRunsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, hotkeyRunLogFileName(now, name))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		path = strings.TrimSuffix(path, ".log") + fmt.Sprintf("-%d.log", os.Getpid())
		f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	}
	if err != nil {
		return nil, "", err
	}
	header := fmt.Sprintf("# %s\n# command: %s\n# started: %s\n\n", name, command, now.Format(time.RFC3339))
	if _, err := f.WriteString(header); err != nil {
		_ = f.Close()
		return nil, "", err
	}
	return f, path, nil
}

func hotkeyRunLogFileName(now time.Time, name string) string {
	return now.Format("20060102-150405") + "-" + sanitizeHotkeyRunName(name) + ".log"
}

func sanitizeHotkeyRunName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "command"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' ||
			r == '"' || r == '<' || r == '>' || r == '|' || unicode.IsControl(r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
		if b.Len() >= 40 {
			break
		}
	}
	out := strings.Trim(b.String(), " ._")
	if out == "" {
		return "command"
	}
	return out
}

func formatRunNotifyMessage(name string, ok bool, exitCode int) string {
	if ok {
		return name + " 执行成功"
	}
	if exitCode >= 0 {
		return name + " 执行失败，退出码 " + strconv.Itoa(exitCode)
	}
	return name + " 执行失败"
}

func formatRunWindowFooter(ok bool, exitCode int, logPath string, runErr error) string {
	var b strings.Builder
	b.WriteByte('\n')
	if ok {
		b.WriteString("完成，退出码 0\n")
	} else if exitCode >= 0 {
		b.WriteString("退出码 ")
		b.WriteString(strconv.Itoa(exitCode))
		b.WriteByte('\n')
	} else if runErr != nil {
		b.WriteString("命令退出异常: ")
		b.WriteString(runErr.Error())
		b.WriteByte('\n')
	} else {
		b.WriteString("执行失败\n")
	}
	if logPath != "" {
		b.WriteString("完整输出: ")
		b.WriteString(logPath)
		b.WriteByte('\n')
	}
	if !ok {
		b.WriteString("窗口保持打开，可手动关闭\n")
	}
	return b.String()
}

func formatRunLogFooter(ok bool, exitCode int, runErr error, finished time.Time) string {
	var b strings.Builder
	b.WriteString("\n# finished: ")
	b.WriteString(finished.Format(time.RFC3339))
	b.WriteByte('\n')
	if ok {
		b.WriteString("# exit: 0\n")
		return b.String()
	}
	if exitCode >= 0 {
		b.WriteString("# exit: ")
		b.WriteString(strconv.Itoa(exitCode))
		b.WriteByte('\n')
	} else {
		b.WriteString("# exit: error\n")
	}
	if runErr != nil {
		b.WriteString("# error: ")
		b.WriteString(runErr.Error())
		b.WriteByte('\n')
	}
	return b.String()
}
