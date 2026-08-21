package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

func TestTunnelLogEnabledDefaultsOn(t *testing.T) {
	cfg := AccessLogConfig{}
	if !TunnelLogEnabled(cfg, "visyc", 8888) {
		t.Fatal("没写过的隧道应当默认开启访问日志")
	}
}

func TestTunnelLogEnabledHonorsExplicitOff(t *testing.T) {
	cfg := AccessLogConfig{
		Tunnels: map[string]AccessLogTunnel{
			"visyc:8888": {Enabled: false},
			"visyc:9999": {Enabled: true},
		},
	}
	if TunnelLogEnabled(cfg, "visyc", 8888) {
		t.Fatal("显式关闭的隧道不该再记日志")
	}
	if !TunnelLogEnabled(cfg, "visyc", 9999) {
		t.Fatal("显式开启的隧道应当记日志")
	}
}

func TestAccessLogRoundTripAndDelete(t *testing.T) {
	isolateHome(t)

	cfg := AccessLogConfig{
		Enabled:    true,
		ListenPort: 17991,
		Tunnels:    map[string]AccessLogTunnel{"visyc:8888": {Enabled: true}},
	}
	if err := SaveAccessLog(cfg); err != nil {
		t.Fatalf("保存访问日志配置失败: %v", err)
	}
	got, err := LoadAccessLog()
	if err != nil {
		t.Fatalf("读取访问日志配置失败: %v", err)
	}
	if !got.Enabled || got.ListenPort != 17991 || !TunnelLogEnabled(got, "visyc", 8888) {
		t.Fatalf("配置往返丢失: %+v", got)
	}

	if err := AppendAccessLog("visyc", 8888, "2026-08-18 01:10:01.000  1.2.3.4  GET /  200  1ms\n"); err != nil {
		t.Fatalf("写访问日志失败: %v", err)
	}
	path := paths.AccessLogFile("visyc", 8888)
	size, err := AccessLogSize("visyc", 8888)
	if err != nil || size <= 0 {
		t.Fatalf("日志大小应当 > 0，实得 %d / %v", size, err)
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "1.2.3.4") {
		t.Fatalf("日志内容不对: %q / %v", body, err)
	}

	if err := DeleteAccessLog("visyc", 8888); err != nil {
		t.Fatalf("删除访问日志失败: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("删除后文件还在: %v", err)
	}
	size, err = AccessLogSize("visyc", 8888)
	if err != nil || size != 0 {
		t.Fatalf("删掉后大小应为 0，实得 %d / %v", size, err)
	}
}

func TestAccessLogFrpcPortUsesListenPortWhenEnabled(t *testing.T) {
	isolateHome(t)
	if err := SaveAccessLog(AccessLogConfig{
		Enabled:    true,
		ListenPort: 17991,
		Tunnels:    map[string]AccessLogTunnel{"acme:3000": {Enabled: false}},
	}); err != nil {
		t.Fatal(err)
	}
	p := Profile{Name: "acme"}
	on := Tunnel{LocalPort: 4000}
	off := Tunnel{LocalPort: 3000}
	if got := AccessLogFrpcPort(p, on); got != 17991 {
		t.Fatalf("默认开启的隧道应改走拦截端口，实得 %d", got)
	}
	if got := AccessLogFrpcPort(p, off); got != 3000 {
		t.Fatalf("关闭记录的隧道应直连本机端口，实得 %d", got)
	}
}

func TestAccessLogFrpcPortPassthroughWhenPluginOff(t *testing.T) {
	isolateHome(t)
	if err := SaveAccessLog(AccessLogConfig{Enabled: false, ListenPort: 17991}); err != nil {
		t.Fatal(err)
	}
	p := Profile{Name: "acme"}
	tunn := Tunnel{LocalPort: 3000}
	if got := AccessLogFrpcPort(p, tunn); got != 3000 {
		t.Fatalf("插件关闭时不应改写端口，实得 %d", got)
	}
}

func TestEnsureTunnelLogDefaultWritesEnabledTrue(t *testing.T) {
	isolateHome(t)
	if err := EnsureTunnelLogDefault("visyc", 8888); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAccessLog()
	if err != nil {
		t.Fatal(err)
	}
	item, ok := cfg.Tunnels["visyc:8888"]
	if !ok || !item.Enabled {
		t.Fatalf("新建隧道应显式记成开启: ok=%v item=%+v", ok, item)
	}
}

func TestAddTunnelRejectsAccessLogListenPort(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	if err := SaveAccessLog(AccessLogConfig{Enabled: true, ListenPort: 3000}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 3000, Subdomain: "web"}); err == nil {
		t.Fatal("不该把隧道开在访问日志拦截器占用的端口上")
	}
}

func TestAccessLogConfigPathIsUnderPluginsDir(t *testing.T) {
	isolateHome(t)
	got := paths.AccessLogConfigFile()
	if !strings.HasPrefix(got, paths.PluginsDir()) {
		t.Fatalf("访问日志配置应落在插件目录下: %s", got)
	}
	if filepath.Base(got) != "config.json" {
		t.Fatalf("配置文件名不对: %s", got)
	}
}

func TestRemoveTunnelDeletesAccessLogFileAndEmptyDir(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	addSub(t, p, 3000, "web")
	if err := EnsureTunnelLogDefault(p.Name, 3000); err != nil {
		t.Fatal(err)
	}
	if err := AppendAccessLog(p.Name, 3000, "2026-08-18 01:10:01.000  1.2.3.4  GET /  200  1ms\n"); err != nil {
		t.Fatal(err)
	}
	path := paths.AccessLogFile(p.Name, 3000)
	dir := filepath.Dir(path)

	if _, _, err := RemoveTunnel(p, 3000); err != nil {
		t.Fatalf("删隧道失败: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("删隧道后日志文件还在: %s", path)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("没有其它日志时，档案日志目录也不该留着: %s", dir)
	}
	cfg, err := LoadAccessLog()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Tunnels[AccessLogKey(p.Name, 3000)]; ok {
		t.Fatal("删隧道后配置里不该还留着这条开关")
	}
}

func TestRemoveTunnelKeepsSiblingAccessLog(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	addSub(t, p, 3000, "web")
	addSub(t, p, 4000, "api")
	if err := AppendAccessLog(p.Name, 3000, "a\n"); err != nil {
		t.Fatal(err)
	}
	if err := AppendAccessLog(p.Name, 4000, "b\n"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := RemoveTunnel(p, 3000); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.AccessLogFile(p.Name, 3000)); !os.IsNotExist(err) {
		t.Fatal("被删那条的日志文件还在")
	}
	if _, err := os.Stat(paths.AccessLogFile(p.Name, 4000)); err != nil {
		t.Fatalf("另一条隧道的日志不该被带走: %v", err)
	}
}

func TestDeleteProfileRemovesAccessLogs(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	addSub(t, p, 3000, "web")
	if err := EnsureTunnelLogDefault(p.Name, 3000); err != nil {
		t.Fatal(err)
	}
	if err := AppendAccessLog(p.Name, 3000, "line\n"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(paths.AccessLogFile(p.Name, 3000))

	if err := DeleteProfile(p.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("删档案后访问日志目录还在: %s", dir)
	}
	cfg, err := LoadAccessLog()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Tunnels[AccessLogKey(p.Name, 3000)]; ok {
		t.Fatal("删档案后配置里不该还留着该档案的开关")
	}
}
