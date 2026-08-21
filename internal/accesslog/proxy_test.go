package accesslog

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openfrees/frp-ngrok/internal/store"
)

func mustPort(t *testing.T, hostport string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(hostport)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestProxyRecordsForwardedIPPathAndStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hello" {
			t.Errorf("后端收到路径 %s，想要 /hello", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(backend.Close)

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	localPort := mustPort(t, backendURL.Host)

	p := store.Profile{
		Name: "acme", Domain: "cpolar.example.com", DomainMode: store.ModeWildcard,
		ServerIP: "1.2.3.4", ServerPort: store.DefaultServerPort,
		VhostPort: store.DefaultVhostPort, Token: "deadbeef",
	}
	if err := store.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrentID(p.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTunnel(p, store.TunnelSpec{LocalPort: localPort, Subdomain: "web"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccessLog(store.AccessLogConfig{Enabled: true, ListenPort: 0}); err != nil {
		t.Fatal(err)
	}

	proxy := New()
	port, err := proxy.Start(0)
	if err != nil {
		t.Fatalf("启动拦截器失败: %v", err)
	}
	t.Cleanup(func() { _ = proxy.Stop() })

	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+itoa(port)+"/hello?q=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "web.cpolar.example.com"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("打拦截器失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("状态码 = %d，想要 201", resp.StatusCode)
	}

	deadline := time.Now().Add(2 * time.Second)
	var body []byte
	for time.Now().Before(deadline) {
		body, err = os.ReadFile(store.AccessLogPath(p.Name, localPort))
		if err == nil && strings.Contains(string(body), "203.0.113.9") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := string(body)
	if !strings.Contains(got, "203.0.113.9") {
		t.Fatalf("应记下真实来源 IP，日志是:\n%s", got)
	}
	if !strings.Contains(got, "GET /hello?q=1") {
		t.Fatalf("应记下方法与路径，日志是:\n%s", got)
	}
	if !strings.Contains(got, " 201 ") {
		t.Fatalf("应记下状态码，日志是:\n%s", got)
	}
}

func TestProxyRecordsPostJSONAndFormAndForwardsBody(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var gotJSON, gotForm []byte
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/login":
			gotJSON = append([]byte(nil), b...)
			w.WriteHeader(http.StatusUnauthorized)
		case "/form":
			gotForm = append([]byte(nil), b...)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(backend.Close)
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	localPort := mustPort(t, backendURL.Host)

	p := store.Profile{
		Name: "acme", Domain: "cpolar.example.com", DomainMode: store.ModeWildcard,
		ServerIP: "1.2.3.4", ServerPort: store.DefaultServerPort,
		VhostPort: store.DefaultVhostPort, Token: "deadbeef",
	}
	if err := store.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrentID(p.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTunnel(p, store.TunnelSpec{LocalPort: localPort, Subdomain: "web"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccessLog(store.AccessLogConfig{Enabled: true, ListenPort: 0}); err != nil {
		t.Fatal(err)
	}

	proxy := New()
	port, err := proxy.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Stop() })

	jsonBody := `{"account":"neo","code":"1234"}`
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+itoa(port)+"/login", strings.NewReader(jsonBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "web.cpolar.example.com"
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("JSON 请求状态码 = %d", resp.StatusCode)
	}
	if string(gotJSON) != jsonBody {
		t.Fatalf("后端应原样收到 JSON，实得 %q", gotJSON)
	}

	formBody := "username=neo&password=secret"
	req, err = http.NewRequest(http.MethodPost, "http://127.0.0.1:"+itoa(port)+"/form", strings.NewReader(formBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "web.cpolar.example.com"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if string(gotForm) != formBody {
		t.Fatalf("后端应原样收到表单，实得 %q", gotForm)
	}

	deadline := time.Now().Add(2 * time.Second)
	var logBody []byte
	for time.Now().Before(deadline) {
		logBody, err = os.ReadFile(store.AccessLogPath(p.Name, localPort))
		if err == nil && strings.Contains(string(logBody), "neo") && strings.Contains(string(logBody), "password=secret") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := string(logBody)
	if !strings.Contains(got, jsonBody) {
		t.Fatalf("应记下 POST JSON 参数，日志是:\n%s", got)
	}
	if !strings.Contains(got, "username=neo") || !strings.Contains(got, "password=secret") {
		t.Fatalf("应记下 POST 表单参数，日志是:\n%s", got)
	}
}

func TestProxySkipsLogWhenTunnelDisabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	localPort := mustPort(t, backendURL.Host)

	p := store.Profile{
		Name: "acme", Domain: "cpolar.example.com", DomainMode: store.ModeWildcard,
		ServerIP: "1.2.3.4", ServerPort: store.DefaultServerPort,
		VhostPort: store.DefaultVhostPort, Token: "deadbeef",
	}
	if err := store.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrentID(p.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTunnel(p, store.TunnelSpec{LocalPort: localPort, Subdomain: "web"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccessLog(store.AccessLogConfig{
		Enabled: true,
		Tunnels: map[string]store.AccessLogTunnel{
			store.AccessLogKey(p.Name, localPort): {Enabled: false},
		},
	}); err != nil {
		t.Fatal(err)
	}

	proxy := New()
	port, err := proxy.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Stop() })

	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+itoa(port)+"/skip", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "web.cpolar.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("关闭记录时请求仍应打到本机服务，实得 %d", resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(store.AccessLogPath(p.Name, localPort)); !os.IsNotExist(err) {
		t.Fatal("这条隧道关了记录，不该写出日志文件")
	}
}
