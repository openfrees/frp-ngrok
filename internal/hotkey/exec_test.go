package hotkey

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestShellArgsUseInteractiveLoginForZshAndBash(t *testing.T) {
	for _, shell := range []string{"/bin/zsh", "/bin/bash"} {
		got := shellArgs(shell, "echo ok")
		want := []string{"-l", "-i", "-c", "echo ok"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("shellArgs(%q) = %v, want %v", shell, got, want)
		}
	}
}

func TestShellArgsKeepPlainShellNonInteractive(t *testing.T) {
	got := shellArgs("/bin/sh", "echo ok")
	want := []string{"-l", "-c", "echo ok"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shellArgs(/bin/sh) = %v, want %v", got, want)
	}
}

func TestShellEnvForcesFrpanelTerminalOutput(t *testing.T) {
	got := shellEnv([]string{"PATH=/bin"})
	want := "FRPANEL_FORCE_TERMINAL=1"
	for _, v := range got {
		if v == want {
			return
		}
	}
	t.Fatalf("shellEnv missing %q: %v", want, got)
}

func TestRunStatusInputRoutesToRegisteredCommand(t *testing.T) {
	var buf bytes.Buffer
	unregisterRunStatusInput := registerRunStatusInput(42, &buf)
	sendRunStatusInput(42, "2\n")
	unregisterRunStatusInput()
	sendRunStatusInput(42, "3\n")

	if got := buf.String(); got != "2\n" {
		t.Fatalf("input routed to command = %q, want %q", got, "2\n")
	}
}

func TestTerminalScriptCreatesTabWhenWindowExists(t *testing.T) {
	script := terminalScript(`printf "ok"`)

	for _, want := range []string{
		`tell application "System Events" to set terminalWasRunning to exists process "Terminal"`,
		`if not terminalWasRunning then`,
		`else if (count of windows) = 0 then`,
		`do script "printf \"ok\""`,
		`tell application "System Events" to keystroke "t" using command down`,
		`do script "printf \"ok\"" in selected tab of front window`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("terminalScript missing %q in:\n%s", want, script)
		}
	}
}
