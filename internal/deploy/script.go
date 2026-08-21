// Package deploy 生成服务端 frps 的一键部署脚本。
package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openfrees/frp-ngrok/internal/frpcbin"
	"github.com/openfrees/frp-ngrok/internal/paths"
	"github.com/openfrees/frp-ngrok/internal/store"
)

const scriptTemplate = `#!/usr/bin/env bash
# frps 服务端一键部署（{{MODE_LABEL}}）
# 目标机: {{SERVER_IP}}   以 root 执行
#
{{CHAIN}}

set -euo pipefail

FRP_VERSION="{{VERSION}}"
FRP_DIR="/www/frp"
BIND_PORT={{BIND_PORT}}
VHOST_PORT={{VHOST_PORT}}

echo "==> [0/6] 检查 ${VHOST_PORT} 端口占用"
if ss -lntp 2>/dev/null | grep -qE ":${VHOST_PORT}\b" && ! ss -lntp 2>/dev/null | grep -E ":${VHOST_PORT}\b" | grep -q frps; then
    echo "    !! ${VHOST_PORT} 被别的程序占用了，换个端口重来"
    ss -lntp | grep -E ":${VHOST_PORT}\b"
    exit 1
fi
echo "    OK"

echo "==> [1/6] 安装 frps v${FRP_VERSION}"
mkdir -p "${FRP_DIR}" && cd "${FRP_DIR}"
if [ ! -x "${FRP_DIR}/frps" ] || ! "${FRP_DIR}/frps" --version 2>/dev/null | grep -q "${FRP_VERSION}"; then
    PKG="frp_${FRP_VERSION}_linux_amd64.tar.gz"
    URL="https://github.com/fatedier/frp/releases/download/v${FRP_VERSION}/${PKG}"
    TMP=$(mktemp -d)
    if ! curl -fsSL -m 180 -o "$TMP/frp.tar.gz" "${URL}"; then
        echo "    GitHub 直连失败，改用加速源"
        curl -fsSL -m 300 -o "$TMP/frp.tar.gz" "https://ghproxy.net/${URL}"
    fi
    tar -zxf "$TMP/frp.tar.gz" -C "$TMP" --strip-components=1
    mv "$TMP/frps" "${FRP_DIR}/frps"
    chmod +x "${FRP_DIR}/frps"
    rm -rf "$TMP"
fi
"${FRP_DIR}/frps" --version

echo "==> [2/6] 写入 frps.toml"
# 定界符加引号：heredoc 内容原样落盘，域名和密钥不会被 shell 当代码执行
cat > "${FRP_DIR}/frps.toml" <<'FRPS_TOML'
bindAddr = "0.0.0.0"
bindPort = {{BIND_PORT}}
vhostHTTPPort = {{VHOST_PORT}}
vhostHTTPTimeout = 300
{{SUBDOMAIN_HOST_LINE}}
proxyBindAddr = "127.0.0.1"
auth.method = "token"
auth.token = "{{TOKEN}}"
log.to = "/www/frp/frps.log"
log.level = "info"
log.maxDays = 7
transport.heartbeatTimeout = 90
transport.maxPoolCount = 10
FRPS_TOML
chmod 600 "${FRP_DIR}/frps.toml"

echo "==> [3/6] 配置 systemd 开机自启"
cat > /etc/systemd/system/frps.service <<'FRPS_UNIT'
[Unit]
Description=frp server
After=network.target
[Service]
Type=simple
Restart=always
RestartSec=5s
ExecStart=/www/frp/frps -c /www/frp/frps.toml
LimitNOFILE=1048576
[Install]
WantedBy=multi-user.target
FRPS_UNIT
systemctl daemon-reload
systemctl enable frps >/dev/null 2>&1
systemctl restart frps
sleep 2

echo "==> [4/6] 放行 ${BIND_PORT}"
if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    firewall-cmd --permanent --add-port=${BIND_PORT}/tcp >/dev/null && firewall-cmd --reload >/dev/null
elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    ufw allow ${BIND_PORT}/tcp >/dev/null
fi

echo "==> [5/6] 服务状态: $(systemctl is-active frps)"
echo "==> [6/6] 监听自检"
ss -lntp | grep -E ":(${BIND_PORT}|${VHOST_PORT})\b" || true

cat <<'TIP'

==================== 服务端完成 ====================
1. 云服务商安全组 + 服务器防火墙，各放行一次 {{BIND_PORT}}/TCP（{{VHOST_PORT}} 不要对外放）
2. DNS 解析 A 记录，指向 {{SERVER_IP}}:
{{TIP_DNS}}
3. 建站绑定域名:
{{TIP_SITE}}
{{TIP_CERT}}
   反向代理到 http://127.0.0.1:{{VHOST_PORT}}
   反代配置里必须有: proxy_set_header Host $host;
TIP
`

// NginxConfig 生成站点反向代理的完整 location 配置。
//
// 这份配置与档案的 vhost 端口绑定，由部署方案直接下发，避免用户拿一份固定
// 端口的仓库示例再手工同步。除了 frps 路由必需的 Host 头，也保留隧道常见的
// WebSocket、长任务、大请求体与 SSE 设置。
func NginxConfig(vhostPort int) string {
	return fmt.Sprintf(`location / {
    proxy_pass http://127.0.0.1:%d;

    # frps 靠 Host 头选择隧道，不能删
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    # WebSocket
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";

    # 长任务与大请求
    proxy_connect_timeout 60s;
    proxy_send_timeout 300s;
    proxy_read_timeout 300s;
    client_max_body_size 100m;

    # SSE / 流式输出
    proxy_buffering off;
}`, vhostPort)
}

// Script 依据档案生成服务端部署脚本内容。
//
// 泛域名与单域名两种模式的差别全在这里收口：frps 要不要 subDomainHost、
// DNS 要加几条、证书能不能走 HTTP 验证，脚本里必须说的和实际配置一致。
//
// 这份脚本会被用户以 root 粘到服务器上执行，所以宁可拒绝生成，
// 也不能把没校验过的值拼进去——上层漏校验时这里是最后一道闸。
func Script(p store.Profile) (string, error) {
	if !store.ValidServerHost(p.ServerIP) {
		return "", fmt.Errorf("服务器地址不合法，拒绝生成部署脚本: %q", p.ServerIP)
	}
	// 无底座是正当状态（隧道各绑独立域名），此时档案本就没有域名可校验；
	// 但只要声称有底座，域名就必须过关——它会被写进 frps 的 subDomainHost。
	if p.HasBase() && !store.ValidDomain(p.Domain) {
		return "", fmt.Errorf("域名不合法，拒绝生成部署脚本: %q", p.Domain)
	}
	if !store.ValidToken(p.Token) {
		return "", fmt.Errorf("连接密钥含不安全字符，拒绝生成部署脚本")
	}
	return buildScript(p), nil
}

func buildScript(p store.Profile) string {
	modeLabel := "泛域名 vhost 模式"
	chain := strings.Join([]string{
		"# 链路: https://<子域名>." + p.Domain,
		"#         -> nginx 泛域名站点 *." + p.Domain + " (443, SSL 终止)",
		"#         -> frps vhost (127.0.0.1:" + fmt.Sprint(p.VhostPort) + ")  按 Host 头路由",
		"#         -> frpc (你的电脑) -> 本机端口",
	}, "\n")
	// frps 只有在需要拼接三级域名时才要 subDomainHost；单域名模式由客户端
	// 用 customDomains 绑定，此时留着它反而会挡掉同后缀的自定义域名。
	subdomainHostLine := `subDomainHost = "` + p.Domain + `"`
	certTip := "   SSL 证书用 Let's Encrypt + DNS 验证（通配符证书只能用这种方式签发）"

	switch {
	case !p.HasBase():
		modeLabel = "无底座 vhost 模式"
		chain = strings.Join([]string{
			"# 链路: https://<各条隧道自己绑的独立域名>",
			"#         -> nginx 各自的站点 (443, SSL 终止)",
			"#         -> frps vhost (127.0.0.1:" + fmt.Sprint(p.VhostPort) + ")  按 Host 头路由",
			"#         -> frpc (你的电脑) -> 本机端口",
		}, "\n")
		// 这行必须真的去掉：只要 subDomainHost 还留着，frps 就会拒收落在它
		// 下面的独立域名（validateDomainConfigForServer），而删底座的人多半
		// 正打算把那片地址改成独立域名来用。
		subdomainHostLine = "# 无底座：隧道各自用 customDomains 绑定，不需要 subDomainHost"
		certTip = "   SSL 证书给每个独立域名各签一张，普通的 HTTP 验证就能签"
	case !p.Wildcard():
		modeLabel = "单域名 vhost 模式"
		chain = strings.Join([]string{
			"# 链路: https://" + p.Domain,
			"#         -> nginx 站点 " + p.Domain + " (443, SSL 终止)",
			"#         -> frps vhost (127.0.0.1:" + fmt.Sprint(p.VhostPort) + ")  按 Host 头路由",
			"#         -> frpc (你的电脑) -> 本机端口",
		}, "\n")
		subdomainHostLine = "# 单域名模式：客户端用 customDomains 绑定 " + p.Domain + "，不需要 subDomainHost"
		certTip = "   SSL 证书用 Let's Encrypt 即可，单域名走默认的 HTTP 验证就能签"
	}

	dnsTip := []string{"     这台服务器没有底座域名，解析跟着各条隧道的独立域名走"}
	if recs := p.DNSRecords(); len(recs) > 0 {
		dnsTip = dnsTip[:0]
		for _, rec := range recs {
			dnsTip = append(dnsTip, fmt.Sprintf("     主机记录 %-10s 记录值 %s   （即 %s）", rec.Host, rec.Value, rec.FQDN))
		}
	}
	siteTip := []string{"     每个独立域名各建一个站点"}
	if sites := p.SiteDomains(); len(sites) > 0 {
		siteTip = siteTip[:0]
		for _, d := range sites {
			siteTip = append(siteTip, "     "+d)
		}
	}

	r := strings.NewReplacer(
		"{{VERSION}}", frpcbin.Version,
		"{{TOKEN}}", p.Token,
		"{{DOMAIN}}", p.Domain,
		"{{SERVER_IP}}", p.ServerIP,
		"{{BIND_PORT}}", fmt.Sprint(p.ServerPort),
		"{{VHOST_PORT}}", fmt.Sprint(p.VhostPort),
		"{{MODE_LABEL}}", modeLabel,
		"{{CHAIN}}", chain,
		"{{SUBDOMAIN_HOST_LINE}}", subdomainHostLine,
		"{{TIP_DNS}}", strings.Join(dnsTip, "\n"),
		"{{TIP_SITE}}", strings.Join(siteTip, "\n"),
		"{{TIP_CERT}}", certTip,
	)
	return r.Replace(scriptTemplate)
}

// Export 把部署脚本落盘并返回文件路径。
func Export(p store.Profile) (string, error) {
	body, err := Script(p)
	if err != nil {
		return "", err
	}
	dir := paths.ServerScriptDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(dir, fmt.Sprintf("deploy-frps-%s.sh", p.Name))
	// 脚本内含 token，限制为仅当前用户可读。
	if err := os.WriteFile(out, []byte(body), 0o700); err != nil {
		return "", err
	}
	return out, nil
}
