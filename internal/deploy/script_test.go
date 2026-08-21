package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openfrees/frp-ngrok/internal/store"
)

func base(mode, domain string) store.Profile {
	return store.Profile{
		Name: "acme", Domain: domain, DomainMode: mode,
		ServerIP: "1.2.3.4", ServerPort: store.DefaultServerPort,
		VhostPort: store.DefaultVhostPort, Token: "deadbeefdeadbeef",
	}
}

func mustScript(t *testing.T, p store.Profile) string {
	t.Helper()
	s, err := Script(p)
	if err != nil {
		t.Fatalf("生成脚本失败: %v", err)
	}
	return s
}

func TestWildcardScript(t *testing.T) {
	s := mustScript(t, base(store.ModeWildcard, "cpolar.example.com"))

	if !strings.Contains(s, `subDomainHost = "cpolar.example.com"`) {
		t.Error("泛域名模式的 frps.toml 必须有 subDomainHost")
	}
	if !strings.Contains(s, "*.cpolar.example.com") {
		t.Error("泛域名模式要提示泛解析与通配符证书")
	}
	if !strings.Contains(s, "DNS 验证") {
		t.Error("通配符证书必须提示走 DNS 验证")
	}
	// 主机记录两条：cpolar 与 *.cpolar
	if !strings.Contains(s, "主机记录 cpolar") || !strings.Contains(s, "主机记录 *.cpolar") {
		t.Errorf("解析记录提示不完整:\n%s", s)
	}
	assertNoPlaceholders(t, s)
}

func TestSingleDomainScript(t *testing.T) {
	s := mustScript(t, base(store.ModeSingle, "www.example.com"))

	if strings.Contains(s, "subDomainHost =") {
		t.Error("单域名模式不该给 frps 配 subDomainHost，否则会挡掉同后缀的自定义域名")
	}
	if strings.Contains(s, "*.www.example.com") {
		t.Error("单域名模式不该出现通配符域名")
	}
	if !strings.Contains(s, "customDomains") {
		t.Error("单域名模式应当说明客户端用 customDomains 绑定")
	}
	if !strings.Contains(s, "HTTP 验证") {
		t.Error("单域名证书应当提示可以走 HTTP 验证")
	}
	assertNoPlaceholders(t, s)
}

// 底座删掉之后照样要能出脚本——正是要靠重跑这份脚本，
// 才能把服务端 frps.toml 里那行 subDomainHost 真的去掉。
func TestNoBaseScript(t *testing.T) {
	s := mustScript(t, base(store.ModeNone, ""))

	if strings.Contains(s, "subDomainHost =") {
		t.Error("无底座时 frps 不能再留 subDomainHost，否则那片域名仍会被它当作自己的地盘")
	}
	if !strings.Contains(s, "无底座") {
		t.Errorf("脚本应当说明这台服务器没有底座:\n%s", s)
	}
	if strings.Contains(s, "主机记录") {
		t.Error("无底座没有档案解析要加，不该列主机记录")
	}
	assertNoPlaceholders(t, s)
}

// 漏替换的占位符会被原样贴进服务器终端，必须当场发现。
func assertNoPlaceholders(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, "{{") || strings.Contains(s, "}}") {
		t.Errorf("脚本里还有没替换的占位符:\n%s", s)
	}
}

// TestScriptIsValidBash 用 bash -n 检查语法。
//
// 这份脚本是拿到线上服务器以 root 执行的，多段 heredoc 拼下来很容易少个结束符，
// 语法错误必须在这里拦住，不能等用户粘到服务器上才发现。
func TestScriptIsValidBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("本机没有 bash，跳过语法检查")
	}
	for _, mode := range []string{store.ModeWildcard, store.ModeSingle, store.ModeNone} {
		t.Run(mode, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "deploy.sh")
			domain := "cpolar.example.com"
			if mode == store.ModeNone {
				domain = ""
			}
			body := mustScript(t, base(mode, domain))
			if err := os.WriteFile(f, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("bash", "-n", f).CombinedOutput(); err != nil {
				t.Fatalf("脚本语法有问题: %v\n%s", err, out)
			}
		})
	}
}
