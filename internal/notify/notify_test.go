package notify

import "testing"

func TestNotificationScriptEscapesQuotes(t *testing.T) {
	got := notificationScript(`隧道"控制台`, `漫剧-发包 执行成功`)
	want := `display notification "漫剧-发包 执行成功" with title "隧道\"控制台"`
	if got != want {
		t.Fatalf("script = %q, want %q", got, want)
	}
}
