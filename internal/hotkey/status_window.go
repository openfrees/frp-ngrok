package hotkey

import (
	"strconv"
	"strings"
	"unicode"
)

const (
	statusWindowWidth  = 460
	statusWindowHeight = 260
	statusWindowRight  = 100
	statusWindowTop    = 100
	statusWindowStep   = 28
	statusWindowSlots  = 7
)

type statusWindowFrame struct {
	X int
	Y int
	W int
	H int
}

func statusWindowFrameFor(screenW, screenH, index int) statusWindowFrame {
	slot := index % statusWindowSlots
	offset := slot * statusWindowStep
	return statusWindowFrame{
		X: screenW - statusWindowRight - statusWindowWidth - offset,
		Y: screenH - statusWindowTop - statusWindowHeight - offset,
		W: statusWindowWidth,
		H: statusWindowHeight,
	}
}

func statusWindowCloseCountdownLine(seconds int) string {
	return "窗口将在 " + string(rune('0'+seconds)) + " 秒后关闭"
}

// runStatusShouldAutoClose 成功才倒计时关窗；失败要留着让人看完错误。
func runStatusShouldAutoClose(ok bool) bool {
	return ok
}

type statusTextColor int

const (
	statusTextDefault statusTextColor = iota
	statusTextRed
	statusTextGreen
	statusTextYellow
	statusTextBlue
	statusTextMagenta
	statusTextCyan
	statusTextWhite
)

type statusTextRun struct {
	Text  string
	Color statusTextColor
	Bold  bool
}

func parseStatusANSIRuns(s string) []statusTextRun {
	style := statusTextRun{}
	var runs []statusTextRun
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		style.Text = b.String()
		runs = appendStatusTextRun(runs, style)
		b.Reset()
		style.Text = ""
	}
	for i := 0; i < len(s); {
		if s[i] != '\x1b' || i+1 >= len(s) || s[i+1] != '[' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := i + 2
		for end < len(s) && !unicode.IsLetter(rune(s[end])) {
			end++
		}
		if end >= len(s) {
			b.WriteString(s[i:])
			break
		}
		if s[end] != 'm' {
			i = end + 1
			continue
		}
		flush()
		style.Color, style.Bold = applyStatusSGR(s[i+2:end], style.Color, style.Bold)
		i = end + 1
	}
	flush()
	return runs
}

func appendStatusTextRun(runs []statusTextRun, run statusTextRun) []statusTextRun {
	if run.Text == "" {
		return runs
	}
	if len(runs) > 0 {
		last := &runs[len(runs)-1]
		if last.Color == run.Color && last.Bold == run.Bold {
			last.Text += run.Text
			return runs
		}
	}
	return append(runs, run)
}

func applyStatusSGR(params string, color statusTextColor, bold bool) (statusTextColor, bool) {
	if params == "" {
		return statusTextDefault, false
	}
	for _, raw := range strings.Split(params, ";") {
		if raw == "" {
			raw = "0"
		}
		code, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		switch code {
		case 0:
			color = statusTextDefault
			bold = false
		case 1:
			bold = true
		case 22:
			bold = false
		case 30, 90:
			color = statusTextDefault
		case 31, 91:
			color = statusTextRed
		case 32, 92:
			color = statusTextGreen
		case 33, 93:
			color = statusTextYellow
		case 34, 94:
			color = statusTextBlue
		case 35, 95:
			color = statusTextMagenta
		case 36, 96:
			color = statusTextCyan
		case 37, 97:
			color = statusTextWhite
		case 39:
			color = statusTextDefault
		}
	}
	return color, bold
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
