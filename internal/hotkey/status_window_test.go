package hotkey

import "testing"

func TestStatusWindowFrameUsesTopRightAndOffsets(t *testing.T) {
	first := statusWindowFrameFor(1440, 900, 0)
	if first != (statusWindowFrame{X: 880, Y: 540, W: 460, H: 260}) {
		t.Fatalf("first frame = %+v", first)
	}

	second := statusWindowFrameFor(1440, 900, 1)
	if second.X != first.X-statusWindowStep || second.Y != first.Y-statusWindowStep {
		t.Fatalf("second frame should be offset from first: first=%+v second=%+v", first, second)
	}
}

func TestStatusWindowCloseCountdownLine(t *testing.T) {
	got := statusWindowCloseCountdownLine(5)
	want := "窗口将在 5 秒后关闭"
	if got != want {
		t.Fatalf("countdown line = %q, want %q", got, want)
	}
}

func TestRunStatusShouldAutoCloseOnlyOnSuccess(t *testing.T) {
	if !runStatusShouldAutoClose(true) {
		t.Fatal("success should auto-close")
	}
	if runStatusShouldAutoClose(false) {
		t.Fatal("failure must keep the window open")
	}
}

func TestParseStatusANSIRunsTurnsSGRIntoStyles(t *testing.T) {
	got := parseStatusANSIRuns("\x1b[0;34m标题\x1b[0m\n\x1b[0;32m请选择\x1b[0m \x1b[1mAdmin\x1b[0m")
	want := []statusTextRun{
		{Text: "标题", Color: statusTextBlue},
		{Text: "\n"},
		{Text: "请选择", Color: statusTextGreen},
		{Text: " "},
		{Text: "Admin", Bold: true},
	}
	if len(got) != len(want) {
		t.Fatalf("runs len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("run %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}
