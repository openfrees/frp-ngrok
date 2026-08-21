package hotkey

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHotkeyRunLogFileNameSanitizesIllegalChars(t *testing.T) {
	now := time.Date(2026, 8, 17, 20, 0, 52, 0, time.Local)
	got := hotkeyRunLogFileName(now, "漫剧-发包")
	want := "20260817-200052-漫剧-发包.log"
	if got != want {
		t.Fatalf("file name = %q, want %q", got, want)
	}
	got = hotkeyRunLogFileName(now, `a/b:c*?`)
	if strings.ContainsAny(got, `/\:*?`) {
		t.Fatalf("illegal chars left in %q", got)
	}
}

func TestFormatRunNotifyMessage(t *testing.T) {
	if got := formatRunNotifyMessage("漫剧-发包", true, 0); got != "漫剧-发包 执行成功" {
		t.Fatalf("success notify = %q", got)
	}
	if got := formatRunNotifyMessage("漫剧-发包", false, 1); got != "漫剧-发包 执行失败，退出码 1" {
		t.Fatalf("fail notify = %q", got)
	}
}

func TestFormatRunWindowFooterKeepsFailedWindowOpen(t *testing.T) {
	okText := formatRunWindowFooter(true, 0, "/tmp/ok.log", nil)
	if !strings.Contains(okText, "完成，退出码 0") {
		t.Fatalf("success footer missing: %q", okText)
	}
	if strings.Contains(okText, "窗口保持打开") {
		t.Fatalf("success footer should not keep window: %q", okText)
	}

	failText := formatRunWindowFooter(false, 1, "/tmp/fail.log", nil)
	for _, want := range []string{"退出码 1", "完整输出: /tmp/fail.log", "窗口保持打开，可手动关闭"} {
		if !strings.Contains(failText, want) {
			t.Fatalf("fail footer missing %q in %q", want, failText)
		}
	}
}

func TestOpenHotkeyRunLogWritesHeader(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 8, 17, 20, 0, 52, 0, time.Local)
	f, path, err := openHotkeyRunLog("漫剧-发包", "echo ok", now)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "# 漫剧-发包") || !strings.Contains(text, "# command: echo ok") {
		t.Fatalf("header = %q", text)
	}
	if filepath.Base(path) != "20260817-200052-漫剧-发包.log" {
		t.Fatalf("path base = %q", filepath.Base(path))
	}
}
