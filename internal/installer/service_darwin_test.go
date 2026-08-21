//go:build darwin

package installer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

func TestEnableAutostartRejectsLegacyLoadFalseSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	useFakeLaunchctl(t, `
case "$1" in
  print) exit 1 ;;
  bootout) exit 0 ;;
  bootstrap)
    echo "Bootstrap failed: 5: Input/output error" >&2
    exit 5
    ;;
  load)
    echo "Load failed: 5: Input/output error" >&2
    exit 0
    ;;
  enable) exit 0 ;;
esac
exit 0
`)

	if err := EnableAutostart(); err == nil {
		t.Fatal("launchctl 明确输出 Load failed 时不能把退出码 0 当成注册成功")
	}
}

func TestRestartServiceRecoversWhenKickstartLosesJob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stateFile := filepath.Join(t.TempDir(), "launchctl-state")
	if err := os.WriteFile(stateFile, []byte("loaded"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FRPANEL_TEST_LAUNCHCTL_STATE", stateFile)
	useFakeLaunchctl(t, `
state="$FRPANEL_TEST_LAUNCHCTL_STATE"
case "$1" in
  print)
    if [ "$(cat "$state" 2>/dev/null)" = "loaded" ]; then
      exit 0
    fi
    exit 1
    ;;
  kickstart)
    echo absent > "$state"
    exit 0
    ;;
  bootout)
    echo absent > "$state"
    exit 0
    ;;
  bootstrap)
    echo loaded > "$state"
    exit 0
    ;;
  enable) exit 0 ;;
esac
exit 0
`)

	if err := RestartService(); err != nil {
		t.Fatal(err)
	}
	if !ServiceLoaded() {
		t.Fatal("kickstart 返回成功但任务消失时，应重新 bootstrap 恢复 launchd 任务")
	}
}

func TestEnableAutostartBootstrapsWhenRunningBinaryIsOutdated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.InstalledBin(), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(paths.InstalledBin())
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePort(startPingServer(t, info.ModTime().Unix()-60)); err != nil {
		t.Fatal(err)
	}

	logFile := filepath.Join(t.TempDir(), "launchctl.log")
	t.Setenv("FRPANEL_TEST_LAUNCHCTL_LOG", logFile)
	useFakeLaunchctl(t, `
log="$FRPANEL_TEST_LAUNCHCTL_LOG"
case "$1" in
  print) exit 0 ;;
  kickstart)
    echo kickstart >> "$log"
    exit 0
    ;;
  bootout) exit 0 ;;
  bootstrap)
    echo bootstrap >> "$log"
    exit 0
    ;;
  enable) exit 0 ;;
esac
exit 0
`)

	if err := EnableAutostart(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "bootstrap\n" {
		t.Fatalf("过期进程必须 bootstrap 重载 plist，实得 %q", got)
	}
}

func TestEnableAutostartKickstartsLoadedButStoppedService(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stateFile := filepath.Join(t.TempDir(), "launchctl-state")
	if err := os.WriteFile(stateFile, []byte("loaded-stopped"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FRPANEL_TEST_LAUNCHCTL_STATE", stateFile)
	useFakeLaunchctl(t, `
state="$FRPANEL_TEST_LAUNCHCTL_STATE"
case "$1" in
  print)
    case "$(cat "$state" 2>/dev/null)" in
      loaded-stopped|running) exit 0 ;;
      *) exit 1 ;;
    esac
    ;;
  kickstart)
    echo running > "$state"
    exit 0
    ;;
esac
exit 0
`)

	if err := EnableAutostart(); err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(state) != "running\n" {
		t.Fatalf("launchd 任务已加载但后台未响应时应主动 kickstart，实得状态 %q", state)
	}
}

func TestStartServiceRecoversWhenKickstartLosesJob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := writePlist(); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(t.TempDir(), "launchctl-state")
	if err := os.WriteFile(stateFile, []byte("loaded"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FRPANEL_TEST_LAUNCHCTL_STATE", stateFile)
	useFakeLaunchctl(t, `
state="$FRPANEL_TEST_LAUNCHCTL_STATE"
case "$1" in
  print)
    if [ "$(cat "$state" 2>/dev/null)" = "loaded" ]; then
      exit 0
    fi
    exit 1
    ;;
  kickstart)
    echo absent > "$state"
    exit 0
    ;;
  bootout)
    echo absent > "$state"
    exit 0
    ;;
  bootstrap)
    echo loaded > "$state"
    exit 0
    ;;
  enable) exit 0 ;;
esac
exit 0
`)

	if err := StartService(); err != nil {
		t.Fatal(err)
	}
	if !ServiceLoaded() {
		t.Fatal("StartService 的 kickstart 假成功后也应重新 bootstrap")
	}
}

func useFakeLaunchctl(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "launchctl")
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
