package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openfrees/frp-ngrok/internal/installer"
	"github.com/openfrees/frp-ngrok/internal/paths"
)

func TestWaitForDaemonRejectsStillRunningOldBinary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.InstalledBin(), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantTime := time.Unix(1_800_000_000, 0)
	if err := os.Chtimes(paths.InstalledBin(), wantTime, wantTime); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		stamp := wantTime.Unix() - 1
		if calls.Add(1) >= 4 {
			stamp = wantTime.Unix()
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"version":"test","binaryStamp":%d}`, stamp)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, rawPort, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	if err := installer.WritePort(port); err != nil {
		t.Fatal(err)
	}

	if got := waitForDaemon(2 * time.Second); got != port {
		t.Fatalf("新版后台接管后应返回端口 %d，实得 %d", port, got)
	}
	if got := calls.Load(); got < 4 {
		t.Fatalf("不能把仍在响应的旧后台当成升级完成，握手次数仅 %d", got)
	}
}

func TestAppBundleLauncherHandsTrayOffInsteadOfBlocking(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	start := strings.Index(body, "func stayInMenuBar")
	if start < 0 {
		t.Fatal("找不到 stayInMenuBar 实现")
	}
	end := strings.Index(body[start:], "\nfunc fromAppBundle")
	if end < 0 {
		t.Fatal("找不到 stayInMenuBar 实现的结束位置")
	}
	body = body[start : start+end]
	if strings.Contains(body, "startTray(") {
		t.Fatal(".app 启动器不能直接进入阻塞的菜单栏循环，否则 Finder 只会唤醒旧包")
	}
	for _, want := range []string{"paths.InstalledBin()", `"tray"`, "Process.Release()"} {
		if !strings.Contains(body, want) {
			t.Fatalf(".app 启动器应把菜单栏交给独立的已安装进程，缺少 %s", want)
		}
	}
}

func TestPackageUsesMigratedLauncherIdentity(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("scripts", "package.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "com.frpngrok.launcher.v2") {
		t.Fatal("new package must use the frp-ngrok launcher bundle identity")
	}
}
