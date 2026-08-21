package installer

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
	"testing"
	"time"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

func TestInstallSelfDoesNotReplaceIdenticalBinary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := InstallSelf(); err != nil {
		t.Fatal(err)
	}
	wantModTime := time.Unix(946684800, 0)
	if err := os.Chtimes(paths.InstalledBin(), wantModTime, wantModTime); err != nil {
		t.Fatal(err)
	}

	if err := InstallSelf(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(paths.InstalledBin())
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(wantModTime) {
		t.Fatalf("相同二进制不应被覆盖并触发后台重启，修改时间变成了 %s", info.ModTime())
	}
}

func TestConsoleURLCarriesRunningBinaryStamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"version":"test","binaryStamp":123456}`)
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

	got, err := url.Parse(ConsoleURL(port, "token-abc"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Query().Get("token") != "token-abc" {
		t.Fatalf("控制台地址丢了访问令牌: %s", got)
	}
	if got.Query().Get("v") != "123456" {
		t.Fatalf("控制台地址应携带运行中二进制标识以强制加载新版，实得 %s", got)
	}
}

func TestLoadedServiceNeedsReloadWhenBinaryStampDiffers(t *testing.T) {
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
	stamp := info.ModTime().Unix()

	if !loadedServiceNeedsReload(0) {
		t.Fatal("没有端口时必须重载")
	}

	current := startPingServer(t, stamp)
	if loadedServiceNeedsReload(current) {
		t.Fatal("磁盘程序与正在跑的是同一份时不该重载")
	}

	stale := startPingServer(t, stamp-60)
	if !loadedServiceNeedsReload(stale) {
		t.Fatal("launchd 还在跑旧二进制时必须 bootstrap 重载 plist，kickstart 换不了 ProgramArguments")
	}
}

func startPingServer(t *testing.T, stamp int64) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"version":"test","binaryStamp":%d}`, stamp)
	}))
	t.Cleanup(srv.Close)
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
	return port
}

func TestServeProcessPatternsIncludeLegacyBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyDir := filepath.Join(home, ".frpanel", "bin")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, paths.LegacyBinaryName), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := strings.Join(serveProcessPatterns(), "\n")
	if !strings.Contains(got, paths.BinaryName+" serve") {
		t.Fatalf("升级后必须能杀掉新二进制进程，got %s", got)
	}
	if !strings.Contains(got, paths.LegacyBinaryName+" serve") {
		t.Fatalf("旧 frpanel 可能仍占着端口，必须一并杀掉，got %s", got)
	}
}
