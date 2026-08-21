package store

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

func seedProfile(t *testing.T, mode, domain string) Profile {
	t.Helper()
	isolateHome(t)
	p := Profile{
		Name: "acme", Domain: domain, DomainMode: mode,
		ServerIP: "1.2.3.4", ServerPort: DefaultServerPort,
		VhostPort: DefaultVhostPort, Token: "deadbeef",
	}
	if err := SaveProfile(p); err != nil {
		t.Fatalf("保存档案失败: %v", err)
	}
	return p
}

func readConf(t *testing.T, p Profile) string {
	t.Helper()
	b, err := os.ReadFile(p.ConfPath())
	if err != nil {
		t.Fatalf("读取 frpc.toml 失败: %v", err)
	}
	return string(b)
}

// addSub 加一条挂在档案域名上的隧道。
func addSub(t *testing.T, p Profile, port int, sub string) {
	t.Helper()
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: port, Subdomain: sub}); err != nil {
		t.Fatalf("新增隧道失败: %v", err)
	}
}

func TestWildcardTunnelUsesSubdomain(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	addSub(t, p, 3000, "web")

	conf := readConf(t, p)
	if !strings.Contains(conf, `subdomain = "web"`) {
		t.Fatalf("泛域名模式应当写 subdomain:\n%s", conf)
	}
	if strings.Contains(conf, "customDomains") {
		t.Fatalf("泛域名模式不该写 customDomains:\n%s", conf)
	}

	tunnels, err := LoadTunnels(p)
	if err != nil || len(tunnels) != 1 {
		t.Fatalf("回读隧道失败: %v / %+v", err, tunnels)
	}
	if got := tunnels[0].PublicURL(p); got != "https://web.cpolar.example.com/" {
		t.Errorf("公网地址 = %s", got)
	}
}

func TestSingleDomainTunnelUsesCustomDomains(t *testing.T) {
	p := seedProfile(t, ModeSingle, "www.example.com")
	// 单域名下子域名参数应当被忽略，而不是拼进地址里
	addSub(t, p, 3000, "web")

	conf := readConf(t, p)
	if !strings.Contains(conf, `customDomains = ["www.example.com"]`) {
		t.Fatalf("单域名模式应当写 customDomains:\n%s", conf)
	}
	if strings.Contains(conf, "subdomain") {
		t.Fatalf("单域名模式不该写 subdomain:\n%s", conf)
	}

	tunnels, err := LoadTunnels(p)
	if err != nil || len(tunnels) != 1 {
		t.Fatalf("回读隧道失败: %v / %+v", err, tunnels)
	}
	if got := tunnels[0].PublicURL(p); got != "https://www.example.com/" {
		t.Errorf("公网地址 = %s", got)
	}
}

// 一个域名只能指向一处，第二条必须在写盘前就被挡住。
func TestSingleDomainRejectsSecondTunnel(t *testing.T) {
	p := seedProfile(t, ModeSingle, "www.example.com")
	addSub(t, p, 3000, "")
	_, err := AddTunnel(p, TunnelSpec{LocalPort: 4000})
	if err == nil {
		t.Fatal("单域名模式下第二条隧道必须被拒")
	}
	if !strings.Contains(err.Error(), "已经指向本机 3000 端口") {
		t.Errorf("报错该说清是谁占着，实得: %v", err)
	}
}

// 单域名模式下，第二条隧道自带域名就该放行——它不占档案域名那个位置。
func TestSingleDomainAcceptsSecondTunnelWithOwnDomain(t *testing.T) {
	p := seedProfile(t, ModeSingle, "www.example.com")
	addSub(t, p, 3000, "")
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 4000, CustomDomain: "api.other.com"}); err != nil {
		t.Fatalf("绑独立域名的第二条隧道应当放行: %v", err)
	}

	conf := readConf(t, p)
	for _, want := range []string{
		`customDomains = ["www.example.com"]`,
		`customDomains = ["api.other.com"]`,
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("配置里缺少 %s:\n%s", want, conf)
		}
	}

	tunnels, err := LoadTunnels(p)
	if err != nil || len(tunnels) != 2 {
		t.Fatalf("回读隧道失败: %v / %+v", err, tunnels)
	}
	for _, tn := range tunnels {
		want := "https://www.example.com/"
		if tn.LocalPort == 4000 {
			want = "https://api.other.com/"
		}
		if got := tn.PublicURL(p); got != want {
			t.Errorf("端口 %d 的地址 = %s，期望 %s", tn.LocalPort, got, want)
		}
	}
}

// 泛域名底座与独立域名并存：两条隧道各走各的寻址方式。
func TestWildcardAndCustomDomainCoexist(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	addSub(t, p, 3000, "web")
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 4000, CustomDomain: "www.other.com"}); err != nil {
		t.Fatalf("独立域名隧道应当能加: %v", err)
	}

	conf := readConf(t, p)
	if !strings.Contains(conf, `subdomain = "web"`) || !strings.Contains(conf, `customDomains = ["www.other.com"]`) {
		t.Fatalf("两种寻址方式应当同时存在:\n%s", conf)
	}

	tunnels, err := LoadTunnels(p)
	if err != nil || len(tunnels) != 2 {
		t.Fatalf("回读隧道失败: %v / %+v", err, tunnels)
	}
	for _, tn := range tunnels {
		if tn.LocalPort == 4000 && tn.Host(p) != "www.other.com" {
			t.Errorf("独立域名隧道地址 = %s", tn.Host(p))
		}
		if tn.LocalPort == 3000 && tn.Host(p) != "web.cpolar.example.com" {
			t.Errorf("底座隧道地址 = %s", tn.Host(p))
		}
	}
}

// frps 拒收底座下面的自定义域名，面板必须在写盘前就说清楚该怎么办。
func TestCustomDomainUnderBaseRejected(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")

	_, err := AddTunnel(p, TunnelSpec{LocalPort: 3000, CustomDomain: "api.cpolar.example.com"})
	if err == nil {
		t.Fatal("底座下面的一级子域名应当被引导去用三级域名")
	}
	if !strings.Contains(err.Error(), "三级域名 api") {
		t.Errorf("报错该给出替代写法，实得: %v", err)
	}

	_, err = AddTunnel(p, TunnelSpec{LocalPort: 3000, CustomDomain: "a.b.cpolar.example.com"})
	if err == nil {
		t.Fatal("底座下面的多级域名必须被拒")
	}
	if !strings.Contains(err.Error(), "多级域名") {
		t.Errorf("报错该点明是多级域名的问题，实得: %v", err)
	}
}

// 两条隧道不能指向同一个公网地址，无论它是三级域名还是独立域名。
func TestDuplicateHostRejected(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 3000, CustomDomain: "www.other.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 4000, CustomDomain: "www.other.com"}); err == nil {
		t.Fatal("同一个独立域名不该能绑两条隧道")
	}
}

// 换底座时，原有独立域名若被新底座罩住，能无损改写的就改写、改不了的要拦住。
func TestConflictWithBase(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 3000, CustomDomain: "www.example.com"}); err != nil {
		t.Fatal(err)
	}

	// 新底座 example.com 会罩住 www.example.com，但它等价于三级域名 www
	next := p
	next.Domain = "example.com"
	bad, err := ConflictWithBase(p, next)
	if err != nil || len(bad) != 0 {
		t.Fatalf("可无损改写的域名不该算冲突: %v / %v", bad, err)
	}
	if err := MigrateConf(p, next); err != nil {
		t.Fatal(err)
	}
	conf := readConf(t, next)
	if !strings.Contains(conf, `subdomain = "www"`) || strings.Contains(conf, "customDomains") {
		t.Fatalf("被底座罩住的域名应当改写成同地址的三级域名:\n%s", conf)
	}

	// 多级的那种没有等价写法，必须拦下来
	deep := seedProfile(t, ModeWildcard, "cpolar.example.com")
	if _, err := AddTunnel(deep, TunnelSpec{LocalPort: 3000, CustomDomain: "a.b.example.com"}); err != nil {
		t.Fatal(err)
	}
	target := deep
	target.Domain = "example.com"
	bad, err = ConflictWithBase(deep, target)
	if err != nil || len(bad) != 1 || bad[0] != "a.b.example.com" {
		t.Fatalf("多级域名必须报冲突，实得 %v / %v", bad, err)
	}
}

// 切模式后老隧道缺 subdomain 或缺 customDomains，重写配置时必须补齐。
func TestEnsureConfRewritesAddressingOnModeSwitch(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	addSub(t, p, 3000, "web")

	before := p
	p.Domain = "www.example.com"
	p.DomainMode = ModeSingle
	if err := SaveProfile(p); err != nil {
		t.Fatalf("保存档案失败: %v", err)
	}
	if err := MigrateConf(before, p); err != nil {
		t.Fatalf("重写配置失败: %v", err)
	}

	conf := readConf(t, p)
	if strings.Contains(conf, "subdomain") || !strings.Contains(conf, `customDomains = ["www.example.com"]`) {
		t.Fatalf("切到单域名后配置没改过来:\n%s", conf)
	}

	// 再切回泛域名：原来的 web 必须找回来，不能悄悄变成端口号，
	// 否则用户发出去的 web.cpolar.example.com 全断。
	back := p
	p.Domain = "cpolar.example.com"
	p.DomainMode = ModeWildcard
	if err := SaveProfile(p); err != nil {
		t.Fatalf("保存档案失败: %v", err)
	}
	if err := MigrateConf(back, p); err != nil {
		t.Fatalf("重写配置失败: %v", err)
	}
	if conf := readConf(t, p); !strings.Contains(conf, `subdomain = "web"`) {
		t.Fatalf("切回泛域名后应当还原原来的三级域名:\n%s", conf)
	}
}

// 域名点换连字符会撞名，撞了的那条 frpc 会一声不响地不启动，必须拆开。
func TestCustomProxyNamesStayUnique(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 3000, CustomDomain: "a.b.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 4000, CustomDomain: "a-b.com"}); err != nil {
		t.Fatalf("两个不同的域名都该能加: %v", err)
	}

	tunnels, err := LoadTunnels(p)
	if err != nil || len(tunnels) != 2 {
		t.Fatalf("回读隧道失败: %v / %+v", err, tunnels)
	}
	if tunnels[0].Name == tunnels[1].Name {
		t.Fatalf("两条隧道撞名了: %s", tunnels[0].Name)
	}
}

// 增删隧道同样是整份重写，手工 proxy 必须在此之前留下备份。
func TestAddAndRemoveBackupForeignProxy(t *testing.T) {
	cases := []struct {
		name string
		act  func(p Profile) error
	}{
		{"新增", func(p Profile) error {
			_, err := AddTunnel(p, TunnelSpec{LocalPort: 4000, Subdomain: "api"})
			return err
		}},
		{"删除", func(p Profile) error {
			_, _, err := RemoveTunnel(p, 3000)
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := seedProfile(t, ModeWildcard, "cpolar.example.com")
			addSub(t, p, 3000, "web")
			manual := readConf(t, p) + `
[[proxies]]
name = "legacy"
type = "http"
localIP = "127.0.0.1"
localPort = 8080
customDomains = ["legacy.example.net"]
hostHeaderRewrite = "127.0.0.1"
`
			if err := os.WriteFile(p.ConfPath(), []byte(manual), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := c.act(p); err != nil {
				t.Fatalf("操作失败: %v", err)
			}
			backup, err := os.ReadFile(p.ConfPath() + ".manual.bak")
			if err != nil {
				t.Fatalf("整份重写前没留下手工配置备份: %v", err)
			}
			if !strings.Contains(string(backup), "hostHeaderRewrite") {
				t.Error("备份里应当完整保留原来的高级字段")
			}
		})
	}
}

// 换底座后两条隧道算出同一个三级域名，必须在落盘前就被拦住。
func TestMigrationHostCollisionRejected(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "old.example.com")
	addSub(t, p, 3000, "app")
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 4000, CustomDomain: "app.new.example.com"}); err != nil {
		t.Fatal(err)
	}

	next := p
	next.Domain = "new.example.com"
	tunnels, err := LoadTunnels(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPlan(next, tunnels); err == nil {
		t.Fatal("独立域名改写后与已有三级域名相撞，必须报错")
	}
	// 落盘这一层也要挡住，不能只靠调用方记得先问一句
	if err := MigrateConf(p, next); err == nil {
		t.Fatal("SaveTunnels 也该拒绝写出撞车的配置")
	}
	if conf := readConf(t, p); !strings.Contains(conf, `customDomains = ["app.new.example.com"]`) {
		t.Fatalf("被拒之后原配置不该被改动:\n%s", conf)
	}
}

// 单域名切到某条隧道已占用的独立域名上，同样是两条 proxy 抢一个地址。
func TestSingleDomainCollidingWithCustomRejected(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	addSub(t, p, 3000, "web")
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 4000, CustomDomain: "www.other.com"}); err != nil {
		t.Fatal(err)
	}

	next := p
	next.Domain = "www.other.com"
	next.DomainMode = ModeSingle
	tunnels, err := LoadTunnels(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPlan(next, tunnels); err == nil {
		t.Fatal("档案域名与已有独立域名重合时必须报错")
	}
}

// 加所有权标记之前写出的配置必须照常认领，隧道不能凭空消失。
func TestLegacyConfWithoutMarkerStillAdopted(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	legacy := `serverAddr = "1.2.3.4"
serverPort = 7000

[[proxies]]
name = "local9999"
type = "http"
localIP = "127.0.0.1"
localPort = 9999
subdomain = "9999"
`
	if err := os.WriteFile(p.ConfPath(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	tunnels, err := LoadTunnels(p)
	if err != nil || len(tunnels) != 1 || tunnels[0].LocalPort != 9999 {
		t.Fatalf("老配置里的隧道没认出来: %v / %+v", err, tunnels)
	}
	if n, err := ForeignProxies(p); err != nil || n != 0 {
		t.Fatalf("老配置不该被当成外来 proxy: %d (err=%v)", n, err)
	}

	// 重写之后补上标记，且地址一字不变
	if err := EnsureConf(p); err != nil {
		t.Fatal(err)
	}
	conf := readConf(t, p)
	if !strings.Contains(conf, `subdomain = "9999"`) || !strings.Contains(conf, `managedBy = "frpanel"`) {
		t.Fatalf("重写后应当保留地址并补上标记:\n%s", conf)
	}
	if _, err := os.Stat(p.ConfPath() + ".manual.bak"); err == nil {
		t.Error("老配置是面板自己写的，不该触发手工配置备份")
	}
}

// 手工写的 proxy 带面板不认识的字段，绝不能被当成自己的悄悄改写。
func TestForeignProxyIsNotAdopted(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	addSub(t, p, 3000, "web")
	manual := readConf(t, p) + `
[[proxies]]
name = "legacy"
type = "http"
localIP = "127.0.0.1"
localPort = 8080
customDomains = ["legacy.example.net"]
locations = ["/api"]
hostHeaderRewrite = "127.0.0.1"
`
	if err := os.WriteFile(p.ConfPath(), []byte(manual), 0o600); err != nil {
		t.Fatal(err)
	}

	tunnels, err := LoadTunnels(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, tn := range tunnels {
		if tn.LocalPort == 8080 {
			t.Fatalf("带 locations 的手工 proxy 不该被当成面板隧道: %+v", tn)
		}
	}
	if n, err := ForeignProxies(p); err != nil || n != 1 {
		t.Fatalf("应当识别出 1 条外来 proxy，实得 %d (err=%v)", n, err)
	}

	// 重写前必须留下原文备份，用户还能自己捡回来
	if err := EnsureConf(p); err != nil {
		t.Fatalf("重写配置失败: %v", err)
	}
	backup, err := os.ReadFile(p.ConfPath() + ".manual.bak")
	if err != nil {
		t.Fatalf("没有留下手工配置备份: %v", err)
	}
	if !strings.Contains(string(backup), "hostHeaderRewrite") {
		t.Error("备份里应当完整保留原来的高级字段")
	}
}

// TestGeneratedConfPassesFrpcVerify 让真实 frpc 校验两种模式生成的配置。
//
// frpc verify 默认开 --strict-config，字段名写错、类型不对都会当场报错，
// 比我们自己断言字符串可靠得多。本机没装 frpc 时跳过。
func TestGeneratedConfPassesFrpcVerify(t *testing.T) {
	// 必须在 seedProfile 改 HOME 之前解析，否则会指到临时目录里去
	bin := paths.FrpcBin()
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("本机没有 frpc（%s），跳过真实配置校验", bin)
	}

	cases := []struct {
		name         string
		mode         string
		domain       string
		subdomain    string
		customDomain string
	}{
		{"wildcard", ModeWildcard, "cpolar.example.com", "web", ""},
		{"single", ModeSingle, "www.example.com", "", ""},
		{"custom", ModeWildcard, "cpolar.example.com", "", "www.other.com"},
		{"nobase", ModeNone, "", "", "www.other.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := seedProfile(t, c.mode, c.domain)
			if _, err := AddTunnel(p, TunnelSpec{
				LocalPort: 3000, Subdomain: c.subdomain, CustomDomain: c.customDomain,
			}); err != nil {
				t.Fatalf("新增隧道失败: %v", err)
			}
			out, err := exec.Command(bin, "verify", "-c", p.ConfPath()).CombinedOutput()
			if err != nil {
				t.Fatalf("frpc 不认这份配置: %v\n%s\n----\n%s", err, out, readConf(t, p))
			}
		})
	}
}

func TestDuplicatePortRejected(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	addSub(t, p, 3000, "web")
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 3000, Subdomain: "other"}); err == nil {
		t.Fatal("同一个端口不该能加两条隧道")
	}
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 4000, Subdomain: "web"}); err == nil {
		t.Fatal("同一个子域名不该能加两条隧道")
	}
}

// 没有底座就没有可挂的地址，这时候只能绑独立域名。
func TestAddTunnelWithoutBase(t *testing.T) {
	p := seedProfile(t, ModeNone, "")

	// 写盘前的 checkPlan 是最后一道闸，去掉这里的拒绝它照样会拦下来，
	// 但那时报的是「配置写不出去」。用户正在新增隧道，该看到的是「没地方挂、
	// 你可以绑独立域名或先建底座」，所以断言要钉住这一层特有的措辞，
	// 只查「底座」两个字的话，两层报错都含它，等于什么也没钉住。
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 3000, Subdomain: "web"}); err == nil {
		t.Fatal("无底座时挂靠隧道应当被拒")
	} else if !strings.Contains(err.Error(), "没地方挂") {
		t.Fatalf("报错该是新增隧道这一层给的，实得 %v", err)
	}
	// 连三级域名都没给的时候同样得拒，不能悄悄写出一条没有地址的 proxy
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 3000}); err == nil {
		t.Fatal("无底座时不给任何域名也应当被拒")
	}

	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 3000, CustomDomain: "www.other.com"}); err != nil {
		t.Fatalf("无底座时绑独立域名应当成功: %v", err)
	}
	conf := readConf(t, p)
	if !strings.Contains(conf, `customDomains = ["www.other.com"]`) {
		t.Fatalf("独立域名没写进配置:\n%s", conf)
	}
	if strings.Contains(conf, "subdomain =") {
		t.Fatalf("无底座时不该写 subdomain，frps 会以「未启用」拒收:\n%s", conf)
	}
}

// 底座没了、隧道还挂着，是最危险的半截状态：frps 拒收这种 proxy，
// 界面上却还列着一条，只有翻日志才看得见。必须写盘前就拦住。
func TestSaveTunnelsRejectsOrphanAfterBaseRemoved(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	addSub(t, p, 3000, "web")
	tunnels, err := LoadTunnels(p)
	if err != nil || len(tunnels) != 1 {
		t.Fatalf("回读隧道失败: %v / %+v", err, tunnels)
	}

	none := p
	none.Domain = ""
	none.DomainMode = ModeNone

	if err := CheckPlan(none, tunnels); err == nil {
		t.Fatal("挂靠隧道还在时，切到无底座应当被 CheckPlan 拦下")
	}
	if err := SaveTunnels(none, tunnels); err == nil {
		t.Fatal("挂靠隧道还在时，无底座配置不该写得出去")
	}
	// 写盘被拒了，磁盘上那份必须还是原来的，不能留半截
	if conf := readConf(t, p); !strings.Contains(conf, `subdomain = "web"`) {
		t.Fatalf("被拒之后原配置不该被动过:\n%s", conf)
	}
}

// RemoveTunnel 要把删掉的那条一并交出来。
//
// 调用方靠它判断「删掉的是哪一类」。少了这个返回值，调用方只能自己再读一次
// 配置去比对前后差异，而那是另一个快照——两份快照之间文件被动过，就会推出
// 一个根本没发生过的因果（比如把「删了条独立域名隧道」误判成「底座被腾空」）。
func TestRemoveTunnelReportsWhichOneWentAway(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	addSub(t, p, 3000, "web")
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 8080, CustomDomain: "www.other.com"}); err != nil {
		t.Fatal(err)
	}

	removed, kept, err := RemoveTunnel(p, 8080)
	if err != nil {
		t.Fatalf("删隧道失败: %v", err)
	}
	if removed.LocalPort != 8080 || !removed.Independent() {
		t.Fatalf("交出来的应当是那条独立域名隧道，实得 %+v", removed)
	}
	if len(kept) != 1 || kept[0].LocalPort != 3000 {
		t.Fatalf("剩下的应当只有挂靠那条，实得 %+v", kept)
	}

	removed, kept, err = RemoveTunnel(p, 3000)
	if err != nil {
		t.Fatalf("删隧道失败: %v", err)
	}
	if removed.LocalPort != 3000 || removed.Independent() {
		t.Fatalf("交出来的应当是那条挂靠隧道，实得 %+v", removed)
	}
	if len(kept) != 0 {
		t.Fatalf("应当一条不剩，实得 %+v", kept)
	}
}

// 独立域名的隧道不依赖底座，底座删掉后它们要能原样迁过去。
func TestMigrateConfKeepsIndependentTunnelsWhenBaseRemoved(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	if _, err := AddTunnel(p, TunnelSpec{LocalPort: 8080, CustomDomain: "www.other.com"}); err != nil {
		t.Fatal(err)
	}

	none := p
	none.Domain = ""
	none.DomainMode = ModeNone
	if err := MigrateConf(p, none); err != nil {
		t.Fatalf("删底座后重写配置失败: %v", err)
	}

	tunnels, err := LoadTunnels(none)
	if err != nil || len(tunnels) != 1 {
		t.Fatalf("独立域名隧道应当原样留着: %v / %+v", err, tunnels)
	}
	if got := tunnels[0].PublicURL(none); got != "https://www.other.com/" {
		t.Errorf("独立域名的地址不该受底座影响，实得 %s", got)
	}
}

func TestSaveTunnelsKeepsOriginPortWhenFrpcPortIsMapped(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	SetFrpcPortMapper(func(Profile, Tunnel) int { return 17991 })
	t.Cleanup(func() { SetFrpcPortMapper(nil) })
	addSub(t, p, 3000, "web")

	conf := readConf(t, p)
	if !strings.Contains(conf, "localPort = 17991") {
		t.Fatalf("映射开启时 frpc 应连拦截端口:\n%s", conf)
	}
	if !strings.Contains(conf, `originPort = "3000"`) {
		t.Fatalf("本机真实端口必须写进 originPort，否则界面会显示拦截端口:\n%s", conf)
	}

	tunnels, err := LoadTunnels(p)
	if err != nil || len(tunnels) != 1 {
		t.Fatalf("回读隧道失败: %v / %+v", err, tunnels)
	}
	if tunnels[0].LocalPort != 3000 {
		t.Fatalf("界面端口应仍是 3000，实得 %d", tunnels[0].LocalPort)
	}
}

func writeMappedProxies(t *testing.T, p Profile, body string) {
	t.Helper()
	conf := `serverAddr = "1.2.3.4"
serverPort = 7000

` + body
	if err := os.WriteFile(p.ConfPath(), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTunnelsRecoversWhenOriginPortEqualsInterceptor(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	if err := SaveAccessLog(AccessLogConfig{Enabled: true, ListenPort: 50622}); err != nil {
		t.Fatal(err)
	}
	writeMappedProxies(t, p, `
[[proxies]]
name = "local9999"
type = "http"
localIP = "127.0.0.1"
localPort = 50622
subdomain = "9999"
metadatas = { managedBy = "frpanel", bind = "base", originPort = "50622" }

[[proxies]]
name = "local8888"
type = "http"
localIP = "127.0.0.1"
localPort = 50622
subdomain = "8888"
metadatas = { managedBy = "frpanel", bind = "base", originPort = "50622" }
`)

	tunnels, err := LoadTunnels(p)
	if err != nil || len(tunnels) != 2 {
		t.Fatalf("回读隧道失败: %v / %+v", err, tunnels)
	}
	got := map[string]int{}
	for _, tn := range tunnels {
		got[tn.Subdomain] = tn.LocalPort
	}
	if got["9999"] != 9999 || got["8888"] != 8888 {
		t.Fatalf("拦截端口写进 originPort 后，界面仍应还原真实本机端口，实得 %+v", got)
	}

	SetFrpcPortMapper(AccessLogFrpcPort)
	t.Cleanup(func() { SetFrpcPortMapper(nil) })
	if err := SaveTunnels(p, tunnels); err != nil {
		t.Fatal(err)
	}
	conf := readConf(t, p)
	if !strings.Contains(conf, `originPort = "9999"`) || !strings.Contains(conf, `originPort = "8888"`) {
		t.Fatalf("重写后必须把真实端口写回 originPort:\n%s", conf)
	}
	if strings.Count(conf, `originPort = "50622"`) != 0 {
		t.Fatalf("重写后不得再把拦截端口当成 originPort:\n%s", conf)
	}
}

func TestLoadTunnelsRecoversMappedPortWhenOriginPortMissing(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	if err := SaveAccessLog(AccessLogConfig{Enabled: true, ListenPort: 17991}); err != nil {
		t.Fatal(err)
	}
	writeMappedProxies(t, p, `
[[proxies]]
name = "local9999"
type = "http"
localIP = "127.0.0.1"
localPort = 17991
subdomain = "9999"
metadatas = { managedBy = "frpanel", bind = "base" }
`)

	tunnels, err := LoadTunnels(p)
	if err != nil || len(tunnels) != 1 || tunnels[0].LocalPort != 9999 {
		t.Fatalf("没有 originPort 时也应从 proxy 名还原端口: %v / %+v", err, tunnels)
	}
}

func TestLoadTunnelsKeepsExplicitOriginWhenNameIsSubdomain(t *testing.T) {
	p := seedProfile(t, ModeWildcard, "cpolar.example.com")
	if err := SaveAccessLog(AccessLogConfig{Enabled: true, ListenPort: 17991}); err != nil {
		t.Fatal(err)
	}
	writeMappedProxies(t, p, `
[[proxies]]
name = "localweb"
type = "http"
localIP = "127.0.0.1"
localPort = 17991
subdomain = "web"
metadatas = { managedBy = "frpanel", bind = "base", originPort = "3000" }
`)

	tunnels, err := LoadTunnels(p)
	if err != nil || len(tunnels) != 1 || tunnels[0].LocalPort != 3000 {
		t.Fatalf("三级域名不是端口号时，应以 originPort 为准: %v / %+v", err, tunnels)
	}
}
