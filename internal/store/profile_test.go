package store

import (
	"os"
	"strings"
	"testing"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

// isolateHome 把数据目录挪到临时目录，避免测试写坏本机真实档案。
// paths 包的 home() 走 os.UserHomeDir()，在类 Unix 上即 $HOME。
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestRootDomainAndHostRecord(t *testing.T) {
	cases := []struct {
		domain string
		root   string
		host   string
	}{
		{"example.com", "example.com", "@"},
		{"cpolar.example.com", "example.com", "cpolar"},
		{"a.b.example.com", "example.com", "a.b"},
		{"example.com.cn", "example.com.cn", "@"},
		{"cpolar.example.com.cn", "example.com.cn", "cpolar"},
		{"example.co.uk", "example.co.uk", "@"},
	}
	for _, c := range cases {
		if got := RootDomain(c.domain); got != c.root {
			t.Errorf("RootDomain(%s) = %s, 期望 %s", c.domain, got, c.root)
		}
		if got := HostRecord(c.domain); got != c.host {
			t.Errorf("HostRecord(%s) = %s, 期望 %s", c.domain, got, c.host)
		}
	}
}

func TestNormalizeDomain(t *testing.T) {
	cases := map[string]string{
		"*.cpolar.example.com": "cpolar.example.com",
		" Cpolar.Example.COM ": "cpolar.example.com",
		"cpolar.example.com.":  "cpolar.example.com",
	}
	for in, want := range cases {
		if got := NormalizeDomain(in); got != want {
			t.Errorf("NormalizeDomain(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// 老档案里没有 domain_mode，读出来必须仍是泛域名，否则升级即改语义。
func TestNormalizeDomainModeDefaultsToWildcard(t *testing.T) {
	if NormalizeDomainMode("") != ModeWildcard {
		t.Fatal("空模式应当按泛域名处理")
	}
	if NormalizeDomainMode("bogus") != ModeWildcard {
		t.Fatal("未知模式应当回落到泛域名")
	}
	if NormalizeDomainMode(ModeSingle) != ModeSingle {
		t.Fatal("单域名模式不应被改写")
	}
}

func TestProfileDomainDerivation(t *testing.T) {
	wild := Profile{Domain: "cpolar.example.com", DomainMode: ModeWildcard, ServerIP: "1.2.3.4"}
	single := Profile{Domain: "www.example.com", DomainMode: ModeSingle, ServerIP: "1.2.3.4"}

	if got := wild.PublicHost("api"); got != "api.cpolar.example.com" {
		t.Errorf("泛域名 PublicHost = %s", got)
	}
	if got := single.PublicHost("api"); got != "www.example.com" {
		t.Errorf("单域名 PublicHost 应忽略子域名，实得 %s", got)
	}
	if got := wild.DisplayDomain(); got != "*.cpolar.example.com" {
		t.Errorf("泛域名展示文案 = %s", got)
	}
	if got := single.DisplayDomain(); got != "www.example.com" {
		t.Errorf("单域名展示文案 = %s", got)
	}

	if got := len(wild.SiteDomains()); got != 2 {
		t.Errorf("泛域名要绑两行域名，实得 %d", got)
	}
	if got := len(single.SiteDomains()); got != 1 {
		t.Errorf("单域名只绑一行，实得 %d", got)
	}

	recs := wild.DNSRecords()
	if len(recs) != 2 || recs[0].Host != "cpolar" || recs[1].Host != "*.cpolar" {
		t.Fatalf("泛域名解析记录不对: %+v", recs)
	}
	if recs[1].FQDN != "*.cpolar.example.com" {
		t.Errorf("泛解析完整域名 = %s", recs[1].FQDN)
	}
	if recs := single.DNSRecords(); len(recs) != 1 || recs[0].Host != "www" {
		t.Fatalf("单域名解析记录不对: %+v", recs)
	}

	// 根域做泛域名时，泛解析的主机记录是裸星号而不是 *.@
	apex := Profile{Domain: "example.com", DomainMode: ModeWildcard, ServerIP: "1.2.3.4"}
	if recs := apex.DNSRecords(); recs[0].Host != "@" || recs[1].Host != "*" {
		t.Fatalf("根域泛解析记录不对: %+v", recs)
	}
}

func TestSaveLoadProfileRoundTripsDomainMode(t *testing.T) {
	isolateHome(t)
	p := Profile{
		Name: "acme", Domain: "www.example.com", DomainMode: ModeSingle,
		ServerIP: "1.2.3.4", ServerPort: DefaultServerPort,
		VhostPort: DefaultVhostPort, Token: "deadbeef",
	}
	if err := SaveProfile(p); err != nil {
		t.Fatalf("保存档案失败: %v", err)
	}
	got, err := LoadProfile("acme")
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if got.DomainMode != ModeSingle || got.Domain != "www.example.com" {
		t.Fatalf("域名设置没存住: %+v", got)
	}
}

// 加模式字段之前写下的 meta.conf 必须照旧按泛域名读出来。
func TestLoadLegacyProfileWithoutDomainMode(t *testing.T) {
	isolateHome(t)
	legacy := "name=old\ndomain=cpolar.example.com\nserver_ip=1.2.3.4\nserver_port=7000\ntoken=abc\nvhost_port=18080\n"
	if err := os.MkdirAll(paths.ProfileDir("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ProfileMeta("old"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfile("old")
	if err != nil {
		t.Fatalf("读取老档案失败: %v", err)
	}
	if !p.Wildcard() {
		t.Fatal("老档案必须按泛域名解读")
	}
}

func TestValidateRejectsBadDomain(t *testing.T) {
	p := Profile{Name: "x", Domain: "nodot", ServerIP: "1.2.3.4",
		ServerPort: 7000, VhostPort: 18080, Token: "t"}
	if err := p.Validate(); err == nil {
		t.Fatal("没有点的域名应当被拒")
	}
	p.DomainMode = ModeSingle
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "完整域名") {
		t.Fatalf("单域名模式的报错应当提示完整域名，实得 %v", err)
	}
}

// 底座删掉之后档案照常可用，只是不再有任何档案域名可拼。
func TestNoBaseProfileDerivation(t *testing.T) {
	isolateHome(t)
	none := Profile{DomainMode: ModeNone, ServerIP: "1.2.3.4"}

	if none.HasBase() {
		t.Fatal("无底座档案的 HasBase 必须为假")
	}
	if none.Wildcard() {
		t.Fatal("无底座不是泛域名，Wildcard 必须为假")
	}
	// 拼出 "api." 这种半截主机名比返回空串危险得多：它看着像个域名，
	// 会一路滑到 frpc.toml 里去，直到用户发现访问不了才知道出事。
	if got := none.PublicHost("api"); got != "" {
		t.Errorf("无底座 PublicHost 应为空串，实得 %q", got)
	}
	if got := none.DisplayDomain(); got != NoBaseLabel {
		t.Errorf("无底座展示文案 = %q", got)
	}
	if got := none.DNSRecords(); len(got) != 0 {
		t.Errorf("无底座不该有解析记录，实得 %+v", got)
	}
	if got := none.SiteDomains(); len(got) != 0 {
		t.Errorf("无底座不该有站点域名，实得 %+v", got)
	}

	// 模式还是泛域名、域名却空了，属于档案被改坏，同样不能当作有底座
	broken := Profile{DomainMode: ModeWildcard, ServerIP: "1.2.3.4"}
	if broken.HasBase() {
		t.Fatal("域名为空时不能算有底座")
	}
}

func TestNormalizeDomainModeKeepsNone(t *testing.T) {
	if NormalizeDomainMode(ModeNone) != ModeNone {
		t.Fatal("无底座模式不应被回落成泛域名")
	}
}

func TestSaveLoadNoBaseProfile(t *testing.T) {
	isolateHome(t)
	p := Profile{
		Name: "acme", DomainMode: ModeNone,
		ServerIP: "1.2.3.4", ServerPort: DefaultServerPort,
		VhostPort: DefaultVhostPort, Token: "deadbeef",
	}
	if err := SaveProfile(p); err != nil {
		t.Fatalf("无底座档案应当能存下: %v", err)
	}
	got, err := LoadProfile("acme")
	if err != nil {
		t.Fatalf("无底座档案应当能读回: %v", err)
	}
	if got.HasBase() || got.DomainMode != ModeNone || got.Domain != "" {
		t.Fatalf("无底座状态没存住: %+v", got)
	}
}

// 无底座就该真的没有域名：留一个读不到、改不了的残值下来，界面显示
// 「未设底座」而配置里还写着旧域名，出问题时无从对账。
func TestValidateRejectsLeftoverDomainInNoBaseMode(t *testing.T) {
	p := Profile{Name: "x", Domain: "cpolar.example.com", DomainMode: ModeNone,
		ServerIP: "1.2.3.4", ServerPort: 7000, VhostPort: 18080, Token: "deadbeef"}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "无底座") {
		t.Fatalf("无底座模式带着域名应当被拒，实得 %v", err)
	}
}

// 「有没有底座」这件事，模式和域名必须说的是同一句话，两个方向都得查。
//
// 只查一半的话，读进来的档案会处在 Validate 明令拒绝的状态：界面按模式显示
// 「未设底座」，meta.conf 里却还写着域名，下次保存直接失败——用户拿到的是
// 一份看得见、改不动、也说不清哪儿不对的档案。
func TestLoadProfileRejectsModeDomainMismatch(t *testing.T) {
	cases := map[string]string{
		"泛域名模式缺 domain":  "domain=\ndomain_mode=wildcard\n",
		"无底座模式留着 domain": "domain=old.example.com\ndomain_mode=none\n",
	}
	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			isolateHome(t)
			broken := "name=bad\n" + pair + "server_ip=1.2.3.4\nserver_port=7000\ntoken=abc\nvhost_port=18080\n"
			if err := os.MkdirAll(paths.ProfileDir("bad"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.ProfileMeta("bad"), []byte(broken), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProfile("bad"); err == nil {
				t.Fatal("模式与域名对不上的档案应当按损坏处理")
			}
		})
	}
}
