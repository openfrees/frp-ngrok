// Package probe 提供连通性探测：端口、DNS 解析与公网 HTTPS 可达性。
//
// 三层分开判断，避免「端口通」被误当成「隧道可用」——
// 端口通只说明对端有程序在听，不代表那是我们的 frps。
package probe

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// DNSResult 是域名解析的判定结果。
type DNSResult string

const (
	// DNSOK 表示解析结果命中目标服务器 IP。
	DNSOK DNSResult = "ok"
	// DNSMismatch 表示有解析记录但不指向目标 IP。
	DNSMismatch DNSResult = "mismatch"
	// DNSMissing 表示没有任何 A 记录。
	DNSMissing DNSResult = "missing"
	// DNSHijacked 表示本机 DNS 被代理接管（fake-ip），无法据此判断。
	DNSHijacked DNSResult = "hijacked"
	// DNSSkipped 表示这台服务器没有底座域名，没有可查的档案解析。
	//
	// 必须与「查不到 A 记录」区分开：无底座是正常状态，报成红叉会让人
	// 跑去 DNS 后台加一条根本不需要的记录。
	DNSSkipped DNSResult = "skipped"
)

// DNSCheck 是一次域名解析检查的结果。
type DNSCheck struct {
	Host   string    `json:"host"`
	Result DNSResult `json:"result"`
	IPs    []string  `json:"ips"`
}

// OK 表示解析结论不构成阻塞（命中目标、无法判断，或压根不需要查）。
func (d DNSCheck) OK() bool {
	return d.Result == DNSOK || d.Result == DNSHijacked || d.Result == DNSSkipped
}

// TCPOpen 检测目标地址端口是否可建立连接。
func TCPOpen(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(port)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// LocalPortInUse 检测本机端口上是否有服务在监听。
func LocalPortInUse(port int) bool {
	return TCPOpen("127.0.0.1", port, 600*time.Millisecond)
}

// TCPResult 是端口探测的判定结果。
type TCPResult string

const (
	// TCPReachable 表示目标端口握手成功。
	//
	// 它不等于「对端那个端口一定有 frps 在听」：握手只能证明有人接了，
	// 接的是不是我们要找的程序，只有登录结果说了算。
	TCPReachable TCPResult = "reachable"
	// TCPUnreachable 表示建不了连接。
	TCPUnreachable TCPResult = "unreachable"
	// TCPHijacked 只用于兼容旧版接口；当前探测不再判断本机代理。
	TCPHijacked TCPResult = "hijacked"
)

// TCPCheck 是一次端口探测的结果。
type TCPCheck struct {
	Result TCPResult `json:"result"`
}

// Unreachable 表示目标端口确实连不上。
func (t TCPCheck) Unreachable() bool { return t.Result == TCPUnreachable }

// CheckTCP 只探测用户填写的目标端口，不推测本机是否使用代理。
func CheckTCP(host string, port int, timeout time.Duration) TCPCheck {
	return checkTCP(host, port, timeout, TCPOpen)
}

func checkTCP(host string, port int, timeout time.Duration, open func(string, int, time.Duration) bool) TCPCheck {
	if !open(host, port, timeout) {
		return TCPCheck{Result: TCPUnreachable}
	}
	return TCPCheck{Result: TCPReachable}
}

// CheckDNS 解析域名并与目标服务器 IP 比对。
func CheckDNS(host, serverIP string, timeout time.Duration) DNSCheck {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out := DNSCheck{Host: host}
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		out.Result = DNSMissing
		return out
	}
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil || ip.To4() == nil {
			continue
		}
		out.IPs = append(out.IPs, a)
	}
	if len(out.IPs) == 0 {
		out.Result = DNSMissing
		return out
	}
	for _, a := range out.IPs {
		// 198.18.0.0/15 是 Clash 等代理的 fake-ip 段，此时解析结果无参考价值。
		if strings.HasPrefix(a, "198.18.") || strings.HasPrefix(a, "198.19.") {
			out.Result = DNSHijacked
			return out
		}
		if a == serverIP {
			out.Result = DNSOK
			return out
		}
	}
	out.Result = DNSMismatch
	return out
}

// HTTPCheck 是一次公网访问探测的结果。
type HTTPCheck struct {
	URL        string `json:"url"`
	StatusCode int    `json:"statusCode"`
	Error      string `json:"error"`
	// HostBlocked 表示请求已到达本机服务，但被对方以「主机名不允许」拒绝。
	HostBlocked bool `json:"hostBlocked"`
	// BlockHint 是针对该开发服务器的具体修复建议。
	BlockHint string `json:"blockHint"`
}

// Reachable 表示拿到了任意 HTTP 响应（链路已打通）。
func (h HTTPCheck) Reachable() bool { return h.StatusCode > 0 }

// CheckHTTP 访问公网地址，判断链路是否打通并识别常见的主机名拦截。
func CheckHTTP(url string, timeout time.Duration) HTTPCheck {
	out := HTTPCheck{URL: url}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			// 证书未就绪时仍希望拿到状态码，用于区分「链路不通」与「证书没配好」。
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		out.Error = simplifyNetErr(err)
		return out
	}
	defer resp.Body.Close()
	out.StatusCode = resp.StatusCode

	// 只读开头一小段用于特征识别，避免把大页面整个拉下来。
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	out.HostBlocked, out.BlockHint = detectHostBlock(string(body))
	return out
}

// devServerBlocks 收录常见开发服务器拒绝陌生 Host 时的特征与修复方法。
//
// 这类响应最容易被误判成「隧道坏了」，其实恰恰说明请求已经通到本机了。
var devServerBlocks = []struct {
	marker string
	hint   string
}{
	{
		marker: "allowedHosts",
		hint:   "本机跑的是 Vite。在 vite.config.js 里加 server.allowedHosts 放行这个域名即可，隧道本身是通的。",
	},
	{
		marker: "Invalid Host header",
		hint:   "本机开发服务器拒绝了陌生域名（webpack-dev-server / Angular 常见）。给它配上 allowedHosts 或 disableHostCheck 即可，隧道本身是通的。",
	},
	{
		marker: "Blocked request",
		hint:   "本机开发服务器按主机名拦下了这次请求。把这个域名加进它的允许列表即可，隧道本身是通的。",
	},
}

func detectHostBlock(body string) (bool, string) {
	for _, b := range devServerBlocks {
		if strings.Contains(body, b.marker) {
			return true, b.hint
		}
	}
	return false, ""
}

func simplifyNetErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no such host"):
		return "域名解析不到"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return "连接超时"
	case strings.Contains(msg, "connection refused"):
		return "连接被拒绝"
	}
	return msg
}

// Explain 把隧道探测结果翻译成可执行的中文建议。
func Explain(dns DNSCheck, http HTTPCheck, serverIP string, localUp bool) string {
	switch dns.Result {
	case DNSMissing:
		return "域名 " + dns.Host + " 没有解析记录，先去 DNS 后台把它的 A 记录指向 " + serverIP
	case DNSMismatch:
		return fmt.Sprintf("域名解析到 %s，不是 %s，检查 DNS 配置",
			strings.Join(dns.IPs, " "), serverIP)
	case DNSHijacked:
		// 本机代理导致解析不可信，继续看 HTTP 结论。
	}
	if !http.Reachable() {
		return "公网访问不通（" + http.Error + "）：检查证书、nginx 反代与 frps 是否都就绪"
	}
	if http.HostBlocked {
		return http.BlockHint
	}
	switch http.StatusCode {
	case 404:
		if localUp {
			return "本机有服务却返回 404：检查 nginx 是否透传 Host 头、子域名是否写对"
		}
		return "链路已通，但本机端口没有服务在跑，起服务即可"
	case 502, 503, 504:
		return "网关错误：nginx 到 frps 这一段有问题，检查反代目标端口"
	}
	return "隧道可用"
}
