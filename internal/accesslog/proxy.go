// Package accesslog 在本机拦截进入隧道的 HTTP 请求，记下 IP、路径、状态码与耗时。
package accesslog

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/openfrees/frp-ngrok/internal/store"
)

// Proxy 是访问日志插件的本机反向代理。
type Proxy struct {
	mu   sync.Mutex
	ln   net.Listener
	srv  *http.Server
	port int
}

// New 创建一个尚未监听的拦截器。
func New() *Proxy { return &Proxy{} }

// Start 开始在 127.0.0.1 上监听。preferred 为 0 或占用失败时改用系统分配的端口。
func (p *Proxy) Start(preferred int) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln != nil && (preferred <= 0 || p.port == preferred) {
		return p.port, nil
	}
	if err := p.stopLocked(); err != nil {
		return 0, err
	}

	addr := "127.0.0.1:0"
	if preferred > 0 {
		addr = fmt.Sprintf("127.0.0.1:%d", preferred)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil && preferred > 0 {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return 0, fmt.Errorf("访问日志拦截器无法监听: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	p.ln = ln
	p.srv = srv
	p.port = port
	go func() { _ = srv.Serve(ln) }()
	return port, nil
}

// Stop 关掉拦截器。未启动时直接返回。
func (p *Proxy) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopLocked()
}

func (p *Proxy) stopLocked() error {
	if p.srv == nil {
		return nil
	}
	err := p.srv.Close()
	p.ln = nil
	p.srv = nil
	p.port = 0
	return err
}

// Port 返回当前监听端口；未启动时为 0。
func (p *Proxy) Port() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.port
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg, err := store.LoadAccessLog()
	if err != nil || !cfg.Enabled {
		http.Error(w, "访问日志未开启", http.StatusServiceUnavailable)
		return
	}
	profile, err := store.ResolveCurrent()
	if err != nil {
		http.Error(w, "没有当前服务器", http.StatusBadGateway)
		return
	}
	tunnels, err := store.LoadTunnels(profile)
	if err != nil {
		http.Error(w, "读取隧道失败", http.StatusBadGateway)
		return
	}
	host := r.Host
	if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = h
	}
	var target *store.Tunnel
	for i := range tunnels {
		if strings.EqualFold(tunnels[i].Host(profile), host) {
			target = &tunnels[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "没有匹配的隧道", http.StatusBadGateway)
		return
	}

	dest, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", target.LocalPort))
	if err != nil {
		http.Error(w, "本机地址不合法", http.StatusBadGateway)
		return
	}

	method := r.Method
	path := r.URL.RequestURI()
	ip := clientIP(r)
	payload := snapshotRequest(r)
	started := time.Now()
	rec := &statusRecorder{ResponseWriter: w}

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(dest)
			pr.Out.Host = pr.In.Host
		},
		FlushInterval: -1,
		ErrorHandler: func(rw http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(rw, "本机服务连不上", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	if !store.TunnelLogEnabled(cfg, profile.Name, target.LocalPort) {
		return
	}
	_ = store.AppendAccessLog(profile.Name, target.LocalPort, FormatLine(started, ip, method, path, rec.status, time.Since(started), payload))
}

// FormatLine 写成控制台日志区能直接展示的一行。payload 是 POST/PUT 等请求体摘要。
func FormatLine(at time.Time, ip, method, path string, status int, d time.Duration, payload string) string {
	if path == "" {
		path = "/"
	}
	if len(path) > 1024 {
		path = path[:1024] + "…"
	}
	if ip == "" {
		ip = "-"
	}
	ms := d.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	line := fmt.Sprintf("%s  %s  %s %s  %d  %dms",
		at.Local().Format("2006-01-02 15:04:05.000"), ip, method, path, status, ms)
	if payload != "" {
		line += "  " + payload
	}
	return line + "\n"
}

func clientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(p)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("不支持 Hijack")
	}
	if s.status == 0 {
		s.status = http.StatusSwitchingProtocols
	}
	return hj.Hijack()
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
