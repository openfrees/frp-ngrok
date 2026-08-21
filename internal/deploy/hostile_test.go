package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openfrees/frp-ngrok/internal/frpcbin"
	"github.com/openfrees/frp-ngrok/internal/store"
)

// TestScriptRejectsHostileInput 确认带 shell 元字符的值根本进不了脚本。
//
// 这份脚本是让用户以 root 粘到服务器上跑的，一旦 $(...)、反引号或引号
// 拼进去，就是一条远程命令执行的路。宁可拒绝生成也不能放行。
func TestScriptRejectsHostileInput(t *testing.T) {
	hostileIP := []string{
		"$(id -un)",
		"1.2.3.4$(touch /tmp/pwned)",
		"`id`",
		`1.2.3.4"; id; #`,
		"1.2.3.4\nrm -rf /",
		"1.2.3.4 && id",
	}
	for _, ip := range hostileIP {
		p := base(store.ModeWildcard, "cpolar.example.com")
		p.ServerIP = ip
		if _, err := Script(p); err == nil {
			t.Errorf("恶意 serverIp 应当被拒: %q", ip)
		}
	}

	hostileToken := []string{
		"$(id -un)",
		"abc`id`def",
		`abc"; id; #`,
		"abc\ndef",
		"短",
	}
	for _, tok := range hostileToken {
		p := base(store.ModeWildcard, "cpolar.example.com")
		p.Token = tok
		if _, err := Script(p); err == nil {
			t.Errorf("恶意 token 应当被拒: %q", tok)
		}
	}

	hostileDomain := []string{"$(id).com", "a b.com", `a".com`, "nodot"}
	for _, d := range hostileDomain {
		p := base(store.ModeWildcard, d)
		if _, err := Script(p); err == nil {
			t.Errorf("恶意域名应当被拒: %q", d)
		}
	}
}

// TestScriptHeredocsAreQuoted 确认脚本里没有会做变量展开的 heredoc。
//
// 就算某天上游漏了校验，带引号的定界符也能保证 heredoc 内容原样落盘、
// 不被 shell 当代码执行——这是纵深防御的第二层。
func TestScriptHeredocsAreQuoted(t *testing.T) {
	s, err := Script(base(store.ModeWildcard, "cpolar.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(s, "\n") {
		i := strings.Index(line, "<<")
		if i < 0 {
			continue
		}
		delim := strings.TrimSpace(line[i+2:])
		if !strings.HasPrefix(delim, "'") {
			t.Errorf("heredoc 定界符没加引号，内容会被 shell 展开: %q", line)
		}
	}
}

// TestQuotedHeredocBlocksInjection 是纵深防御的实测。
//
// 上面的校验是第一道闸，这里验证第二道：即便某天有人绕过校验，
// 带引号的 heredoc 也必须让 $(...) 原样落盘、而不是执行。
// 所以这里故意跳过 Script 的校验，直接用 buildScript 造一份含注入的脚本，
// 在沙箱里真跑一遍——bash -n 是查不出命令替换的。
func TestQuotedHeredocBlocksInjection(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("本机没有 bash，跳过")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")

	p := base(store.ModeWildcard, "cpolar.example.com")
	p.Token = "x$(touch " + marker + ")y"
	body := runnableScript(t, buildScript(p), dir)

	script := filepath.Join(dir, "deploy.sh")
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, _ := cmd.CombinedOutput()

	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("注入被执行了！heredoc 没挡住命令替换\n%s", out)
	}
	conf, err := os.ReadFile(filepath.Join(dir, "frp", "frps.toml"))
	if err != nil {
		t.Fatalf("脚本没写出 frps.toml: %v\n%s", err, out)
	}
	// 原样落盘才说明 shell 没碰它
	if !strings.Contains(string(conf), `auth.token = "x$(touch `) {
		t.Errorf("密钥没有原样写入，说明发生了展开:\n%s", conf)
	}
}

// TestScriptWritesExpectedConfig 真跑一遍正常脚本，确认它写出的 frps.toml 就是我们要的。
func TestScriptWritesExpectedConfig(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("本机没有 bash，跳过")
	}
	dir := t.TempDir()
	body := mustScript(t, base(store.ModeWildcard, "cpolar.example.com"))
	script := filepath.Join(dir, "deploy.sh")
	if err := os.WriteFile(script, []byte(runnableScript(t, body, dir)), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, _ := cmd.CombinedOutput()

	conf, err := os.ReadFile(filepath.Join(dir, "frp", "frps.toml"))
	if err != nil {
		t.Fatalf("脚本没写出 frps.toml: %v\n%s", err, out)
	}
	for _, want := range []string{
		`subDomainHost = "cpolar.example.com"`,
		`auth.token = "deadbeefdeadbeef"`,
		"bindPort = 7000",
		"vhostHTTPPort = 18080",
	} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("frps.toml 缺少 %q:\n%s", want, conf)
		}
	}
}

// runnableScript 把脚本改造成可以在沙箱里安全执行的版本：
// 目录挪到临时目录，有副作用的系统命令换成桩，并预置一个假的 frps 跳过下载。
func runnableScript(t *testing.T, body, dir string) string {
	t.Helper()
	stub := "#!/usr/bin/env bash\nexit 0\n"
	for _, name := range []string{"ss", "systemctl", "curl", "tar", "firewall-cmd", "ufw"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(stub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	frpDir := filepath.Join(dir, "frp")
	if err := os.MkdirAll(frpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 版本对得上，脚本就会跳过整个下载安装分支
	fake := "#!/usr/bin/env bash\necho " + frpcbin.Version + "\n"
	if err := os.WriteFile(filepath.Join(frpDir, "frps"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	body = strings.ReplaceAll(body, "/www/frp", frpDir)
	return strings.ReplaceAll(body, "/etc/systemd/system/frps.service",
		filepath.Join(dir, "frps.service"))
}
