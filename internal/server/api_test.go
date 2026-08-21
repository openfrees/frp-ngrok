package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/openfrees/frp-ngrok/internal/probe"
	"github.com/openfrees/frp-ngrok/internal/store"
	"github.com/openfrees/frp-ngrok/internal/supervisor"
)

// newTestServer 起一个不监听端口、数据目录隔离在临时 HOME 的控制台实例。
//
// 临时 HOME 里没有 frpc 可执行文件，所以 supervisor.Start 只会返回
// 「frpc 程序不存在」而不会真的拉起进程，测试不会碰到本机正在跑的隧道。
func newTestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s, err := New(supervisor.New(), 0, "test")
	if err != nil {
		t.Fatalf("初始化控制台失败: %v", err)
	}
	t.Cleanup(func() {
		_ = s.access.Stop()
		if s.sites != nil {
			_ = s.sites.StopAll()
		}
	})
	return s
}

func call(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	// 控制台只认本机 Host，且写接口一律要令牌
	r.Host = "127.0.0.1"
	r.Header.Set("Authorization", "Bearer "+s.token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestStaticAssetsAreNeverReusedAcrossPackageUpdates(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	r.Host = "127.0.0.1"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("读取页面资源失败: %d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("静态页面必须禁止缓存，避免新后台仍显示旧前端，实得 %q", got)
	}
	r2 := httptest.NewRequest(http.MethodGet, "/i18n.js", nil)
	r2.Host = "127.0.0.1"
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("语言包必须随二进制一起分发: %d", w2.Code)
	}
}

func TestStateDefaultsToEnglishLocale(t *testing.T) {
	s := newTestServer(t)
	w := call(t, s, "GET", "/api/state", "")
	if w.Code != http.StatusOK {
		t.Fatalf("读状态失败: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"locale":"en"`) {
		t.Fatalf("新安装默认语言应是 en:\n%s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"hotkeys":false`) {
		t.Fatalf("状态应带上快捷键插件开关，菜单栏才画得出勾选:\n%s", w.Body.String())
	}
}

func TestPutPrefsPersistsLocaleAndShowsUpInState(t *testing.T) {
	s := newTestServer(t)
	put := call(t, s, "PUT", "/api/prefs", `{"locale":"zh-CN"}`)
	if put.Code != http.StatusOK {
		t.Fatalf("保存语言失败: %d %s", put.Code, put.Body.String())
	}
	if !strings.Contains(put.Body.String(), `"locale":"zh-CN"`) {
		t.Fatalf("保存回执应带回 zh-CN:\n%s", put.Body.String())
	}
	st := call(t, s, "GET", "/api/state", "")
	if !strings.Contains(st.Body.String(), `"locale":"zh-CN"`) {
		t.Fatalf("状态应记住用户改过的语言:\n%s", st.Body.String())
	}
	bad := call(t, s, "PUT", "/api/prefs", `{"locale":"fr"}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("未知语言应拒绝，实得 %d %s", bad.Code, bad.Body.String())
	}
	again := call(t, s, "GET", "/api/prefs", "")
	if !strings.Contains(again.Body.String(), `"locale":"zh-CN"`) {
		t.Fatalf("被拒后已保存的中文不应被冲掉:\n%s", again.Body.String())
	}
}

func TestGetHotkeysIncludesDefaultPaletteCombo(t *testing.T) {
	s := newTestServer(t)
	w := call(t, s, "GET", "/api/plugins/hotkeys", "")
	if w.Code != http.StatusOK {
		t.Fatalf("读取快捷键状态失败: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"paletteCombo":"`+store.DefaultPaletteCombo+`"`) {
		t.Fatalf("快捷键状态缺少默认命令面板触发键:\n%s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"orderVersion":0`) {
		t.Fatalf("快捷键状态缺少排序版本:\n%s", w.Body.String())
	}
}

func mustCreateProfile(t *testing.T, s *Server, body string) store.Profile {
	t.Helper()
	if w := call(t, s, "POST", "/api/profiles", body); w.Code != http.StatusOK {
		t.Fatalf("创建档案失败: %d %s", w.Code, w.Body.String())
	}
	p, err := store.ResolveCurrent()
	if err != nil {
		t.Fatalf("读取当前档案失败: %v", err)
	}
	return p
}

func TestUpdateDomainRequiresToken(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest("PUT", "/api/profiles/x", strings.NewReader("{}"))
	r.Host = "127.0.0.1"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("没带令牌应当 401，实得 %d", w.Code)
	}
}

func TestUpdateDomainSwitchesModeAndRewritesConf(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)
	if _, err := store.AddTunnel(p, store.TunnelSpec{LocalPort: 3000, Subdomain: "web"}); err != nil {
		t.Fatalf("加隧道失败: %v", err)
	}

	w := call(t, s, "PUT", "/api/profiles/"+p.Name,
		`{"serverIp":"5.6.7.8","domain":"www.example.com","domainMode":"single"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("切换模式应当成功，实得 %d %s", w.Code, w.Body.String())
	}

	got, err := store.LoadProfile(p.Name)
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if got.Wildcard() || got.Domain != "www.example.com" || got.ServerIP != "5.6.7.8" {
		t.Fatalf("域名设置没落盘: %+v", got)
	}

	// 落盘的重点是配置也跟着重写了，否则客户端还在用旧的寻址方式
	conf, err := os.ReadFile(got.ConfPath())
	if err != nil {
		t.Fatalf("读取 frpc.toml 失败: %v", err)
	}
	if !strings.Contains(string(conf), `customDomains = ["www.example.com"]`) {
		t.Fatalf("配置没按新模式重写:\n%s", conf)
	}
}

// 编辑服务器只提交连接信息，不能把用户刚设好的域名模式冲掉。
func TestUpdateServerKeepsDomainSettings(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"www.example.com","domainMode":"single"}`)
	oldToken := p.Token

	w := call(t, s, "PUT", "/api/profiles/"+p.Name,
		`{"serverIp":"5.6.7.8","serverPort":7001,"token":"newtoken-123456"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("只改服务器应当成功，实得 %d %s", w.Code, w.Body.String())
	}

	got, err := store.LoadProfile(p.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Domain != "www.example.com" || got.Wildcard() {
		t.Fatalf("域名设置被连带改掉了: %+v", got)
	}
	if got.ServerIP != "5.6.7.8" || got.ServerPort != 7001 {
		t.Fatalf("连接信息没落盘: %+v", got)
	}
	if got.Token == oldToken || got.Token != "newtoken-123456" {
		t.Fatalf("密钥没换成新的: %s", got.Token)
	}
}

// 留空的字段一律不动，别把没填当成要清空。
func TestUpdateServerIgnoresBlankFields(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)

	w := call(t, s, "PUT", "/api/profiles/"+p.Name, `{"domain":"other.example.com"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("只改域名应当成功，实得 %d %s", w.Code, w.Body.String())
	}
	got, err := store.LoadProfile(p.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerIP != "1.2.3.4" || got.ServerPort != p.ServerPort || got.Token != p.Token {
		t.Fatalf("没填的连接信息被改动了: %+v", got)
	}
	if got.Domain != "other.example.com" || !got.Wildcard() {
		t.Fatalf("域名没改对，或模式被莫名切换: %+v", got)
	}
}

// 新底座会罩住已有的独立域名时必须拦下来，否则那条隧道会被 frps 静默拒收。
func TestUpdateDomainRejectsBaseSwallowingCustomDomain(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)
	if _, err := store.AddTunnel(p, store.TunnelSpec{
		LocalPort: 3000, CustomDomain: "a.b.example.com",
	}); err != nil {
		t.Fatal(err)
	}

	w := call(t, s, "PUT", "/api/profiles/"+p.Name,
		`{"domain":"example.com","domainMode":"wildcard"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("底座罩住独立域名时应当被拒，实得 %d %s", w.Code, w.Body.String())
	}
	if got, _ := store.LoadProfile(p.Name); got.Domain != "cpolar.example.com" {
		t.Fatalf("被拒后档案不该改动: %+v", got)
	}
}

// 独立域名隧道要能从接口建出来，并在状态里带上自己的域名。
func TestCreateTunnelWithCustomDomain(t *testing.T) {
	s := newTestServer(t)
	mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)

	if w := call(t, s, "POST", "/api/tunnels",
		`{"localPort":4000,"customDomain":"www.other.com"}`); w.Code != http.StatusOK {
		t.Fatalf("新增独立域名隧道失败: %d %s", w.Code, w.Body.String())
	}

	w := call(t, s, "GET", "/api/state", "")
	if w.Code != http.StatusOK {
		t.Fatalf("读状态失败: %d", w.Code)
	}
	for _, want := range []string{
		`"customDomain":"www.other.com"`,
		`"host":"www.other.com"`,
		`"url":"https://www.other.com/"`,
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("状态里缺少 %s:\n%s", want, w.Body.String())
		}
	}
}

// 接入向导选择单域名时，域名与端口是一个完整意图：保存服务器的同时，
// 就应当得到第一条可用隧道，而不是留下一个看得见却没有任何映射的空底座。
func TestCreateSingleProfileWithInitialTunnel(t *testing.T) {
	s := newTestServer(t)
	w := call(t, s, "POST", "/api/profiles",
		`{"serverIp":"1.2.3.4","domain":"pan.example.com","domainMode":"single","localPort":3000}`)
	if w.Code != http.StatusOK {
		t.Fatalf("创建单域名服务器与首条隧道失败: %d %s", w.Code, w.Body.String())
	}

	p, err := store.ResolveCurrent()
	if err != nil {
		t.Fatal(err)
	}
	tunnels, err := store.LoadTunnels(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(tunnels) != 1 || tunnels[0].LocalPort != 3000 || tunnels[0].Host(p) != "pan.example.com" {
		t.Fatalf("首条隧道没有随服务器一起创建: %+v", tunnels)
	}
}

// 从已经在用的单域名建立泛域名底座时，旧地址必须变成独立域名继续服务；
// 不能把它按新底座重算成 3000.tunnel.example.net，导致用户原地址凭空消失。
func TestSwitchSingleToWildcardPreservesExistingAddress(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"pan.example.com","domainMode":"single"}`)
	if _, err := store.AddTunnel(p, store.TunnelSpec{LocalPort: 3000}); err != nil {
		t.Fatal(err)
	}

	w := call(t, s, "PUT", "/api/profiles/"+p.Name,
		`{"domain":"tunnel.example.net","domainMode":"wildcard","preserveSingleDomain":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("建立泛域名底座失败: %d %s", w.Code, w.Body.String())
	}

	got, err := store.LoadProfile(p.Name)
	if err != nil {
		t.Fatal(err)
	}
	tunnels, err := store.LoadTunnels(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(tunnels) != 1 || tunnels[0].Host(got) != "pan.example.com" || !tunnels[0].Independent() {
		t.Fatalf("切换泛域名后旧地址没有保留下来: %+v", tunnels)
	}
}

// 底座下面的域名不该走独立域名这条路，接口要把替代写法告诉用户。
func TestCreateTunnelRejectsDomainUnderBase(t *testing.T) {
	s := newTestServer(t)
	mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)

	w := call(t, s, "POST", "/api/tunnels",
		`{"localPort":4000,"customDomain":"api.cpolar.example.com"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("底座下面的域名应当被拒，实得 %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "三级域名") {
		t.Errorf("报错该给出替代写法:\n%s", w.Body.String())
	}
}

func TestUpdateDomainRejectsSingleWithManyTunnels(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)
	if _, err := store.AddTunnel(p, store.TunnelSpec{LocalPort: 3000, Subdomain: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTunnel(p, store.TunnelSpec{LocalPort: 4000, Subdomain: "b"}); err != nil {
		t.Fatal(err)
	}

	w := call(t, s, "PUT", "/api/profiles/"+p.Name,
		`{"serverIp":"1.2.3.4","domain":"www.example.com","domainMode":"single"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("两条隧道时切单域名应当被拒，实得 %d %s", w.Code, w.Body.String())
	}

	// 被拒之后档案必须原样不动，不能改了一半
	got, _ := store.LoadProfile(p.Name)
	if !got.Wildcard() || got.Domain != "cpolar.example.com" {
		t.Fatalf("请求被拒后档案不该被改动: %+v", got)
	}
}

func TestUpdateDomainRejectsWildcardInputInSingleMode(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)

	w := call(t, s, "PUT", "/api/profiles/"+p.Name,
		`{"serverIp":"1.2.3.4","domain":"*.foo.example.com","domainMode":"single"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("单域名模式填通配符应当被拒，实得 %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "通配符") {
		t.Errorf("报错该点明原因，实得 %s", w.Body.String())
	}
}

// activate=false 时新档案只落盘，不能把正在跑的连接抢走。
func TestCreateProfileWithoutActivateKeepsCurrent(t *testing.T) {
	s := newTestServer(t)
	first := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)

	w := call(t, s, "POST", "/api/profiles",
		`{"serverIp":"5.6.7.8","domain":"www.other.com","domainMode":"single","activate":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("新建失败: %s", w.Body.String())
	}
	if got := store.CurrentID(); got != first.Name {
		t.Fatalf("当前档案被抢走了：期望 %s，实得 %s", first.Name, got)
	}
	if ids := store.ListProfileIDs(); len(ids) != 2 {
		t.Fatalf("应当有两个档案，实得 %v", ids)
	}
}

// 一个档案都没有时，即便传 activate=false 也必须认领当前位，否则控制台还是空状态。
func TestFirstProfileAlwaysBecomesCurrent(t *testing.T) {
	s := newTestServer(t)
	w := call(t, s, "POST", "/api/profiles",
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard","activate":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("新建失败: %s", w.Body.String())
	}
	if store.CurrentID() == "" {
		t.Fatal("第一个档案必须成为当前档案")
	}
}

// 部署页的解析记录、站点域名、证书说法都由后端下发，前端不再自己算。
func TestDeployScriptCarriesDomainPlan(t *testing.T) {
	s := newTestServer(t)
	mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)

	w := call(t, s, "GET", "/api/deploy-script", "")
	if w.Code != http.StatusOK {
		t.Fatalf("拉取部署方案失败: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`"domainMode":"wildcard"`,
		`"rootDomain":"example.com"`,
		`"host":"*.cpolar"`,
		`"fqdn":"*.cpolar.example.com"`,
		`"siteDomains":["cpolar.example.com","*.cpolar.example.com"]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("部署方案缺少 %s:\n%s", want, body)
		}
	}
}

// 旧的独立 nginx 示例删除后，部署方案必须接管其中仍有效的完整
// 反代配置；不能只留一句 Host 头提示，否则 WebSocket、长任务、大请求与 SSE
// 会在用户真正使用时才暴露问题。
func TestDeployPlanCarriesFullNginxConfig(t *testing.T) {
	s := newTestServer(t)
	mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard","vhostPort":19090}`)

	w := call(t, s, "GET", "/api/deploy-script", "")
	if w.Code != http.StatusOK {
		t.Fatalf("拉取部署方案失败: %d %s", w.Code, w.Body.String())
	}
	var plan struct {
		NginxConfig string `json:"nginxConfig"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil {
		t.Fatalf("解析部署方案失败: %v", err)
	}
	for _, want := range []string{
		"proxy_pass http://127.0.0.1:19090;",
		"proxy_set_header Host $host;",
		"proxy_set_header Upgrade $http_upgrade;",
		"proxy_read_timeout 300s;",
		"client_max_body_size 100m;",
		"proxy_buffering off;",
	} {
		if !strings.Contains(plan.NginxConfig, want) {
			t.Errorf("nginx 配置缺少 %q:\n%s", want, plan.NginxConfig)
		}
	}
}

// 部署页要能查任意一台已接入的服务器，不限于当前连着的那台。
//
// 密钥那条断言是本用例的重点：拿到的必须是该档案自己落盘的 token。
// 若有人图省事把这个接口改成按请求参数现拼，token 就只能临时生成，
// 用户拿着它去服务器重跑，两端就永远对不上，
// 表现为 frpc 一直登录失败——这种错极难从现象反推回来，用测试钉死。
func TestProfileDeployPlanUsesThatProfilesToken(t *testing.T) {
	s := newTestServer(t)
	cur := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"www.alpha.com","domainMode":"single"}`)

	// 第二台只落盘、不接管连接，模拟「当前连着 A，却要看 B 的部署方案」
	w := call(t, s, "POST", "/api/profiles",
		`{"serverIp":"5.6.7.8","domain":"www.beta.com","domainMode":"single","activate":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("建第二个档案失败: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Profile struct {
			Name string `json:"name"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析新建响应失败: %v", err)
	}
	other, err := store.LoadProfile(created.Profile.Name)
	if err != nil {
		t.Fatalf("读取第二个档案失败: %v", err)
	}
	if other.Name == cur.Name {
		t.Fatalf("两个档案重名了，用例前提不成立: %s", other.Name)
	}

	w = call(t, s, "GET", "/api/profiles/"+other.Name+"/deploy-plan", "")
	if w.Code != http.StatusOK {
		t.Fatalf("取部署方案应当成功，实得 %d %s", w.Code, w.Body.String())
	}
	var plan struct {
		Script string `json:"script"`
		Domain string `json:"domain"`
		Token  string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil {
		t.Fatalf("解析部署方案失败: %v", err)
	}

	if plan.Domain != "www.beta.com" {
		t.Fatalf("取成了别的档案的方案: %s", plan.Domain)
	}
	if plan.Token != other.Token {
		t.Fatalf("方案里的密钥与档案对不上，用户拿去重跑会一直登录失败\n档案里 %q\n方案里 %q",
			other.Token, plan.Token)
	}
	if !strings.Contains(plan.Script, other.Token) {
		t.Fatal("脚本正文里没写入该档案的密钥")
	}
	if cur.Token != "" && strings.Contains(plan.Script, cur.Token) {
		t.Fatal("脚本里混进了当前档案的密钥")
	}
}

func TestProfileDeployPlanRejectsUnknownProfile(t *testing.T) {
	s := newTestServer(t)
	mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"www.alpha.com","domainMode":"single"}`)
	if w := call(t, s, "GET", "/api/profiles/nosuch/deploy-plan", ""); w.Code != http.StatusNotFound {
		t.Fatalf("不存在的档案应当 404，实得 %d %s", w.Code, w.Body.String())
	}
}

// ---------- 底座的删除与重建 ----------

// 底座下面还挂着隧道就不能删：删了它们全都没地址，frps 会静默拒收。
func TestDeleteBaseRejectedWhileTunnelsHangOnIt(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)
	if _, err := store.AddTunnel(p, store.TunnelSpec{LocalPort: 3000, Subdomain: "web"}); err != nil {
		t.Fatal(err)
	}

	w := call(t, s, "PUT", "/api/profiles/"+p.Name, `{"domainMode":"none"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("底座下有隧道时删底座应当被拒，实得 %d %s", w.Code, w.Body.String())
	}
	// 写盘前的 CheckPlan 是最后一道闸，去掉这里的前置校验它照样会拦成 400，
	// 但那时给的是一句讲配置的错。用户正在删底座，该看到的是「还有几条挂在
	// 上面、先怎么处理」，所以断言要钉住这一层特有的措辞而不只是状态码。
	if !strings.Contains(w.Body.String(), "底座一删它们就没地址了") {
		t.Errorf("报错该点明是哪几条隧道挡着，实得 %s", w.Body.String())
	}
	got, _ := store.LoadProfile(p.Name)
	if !got.HasBase() || got.Domain != "cpolar.example.com" {
		t.Fatalf("被拒之后底座不该被动过: %+v", got)
	}
}

// 底座空着就能删，删完档案进入无底座，独立域名的隧道原样留着。
func TestDeleteEmptyWildcardBase(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)
	if _, err := store.AddTunnel(p, store.TunnelSpec{
		LocalPort: 8080, CustomDomain: "www.other.com",
	}); err != nil {
		t.Fatal(err)
	}

	w := call(t, s, "PUT", "/api/profiles/"+p.Name, `{"domainMode":"none"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("空底座应当能删，实得 %d %s", w.Code, w.Body.String())
	}

	got, err := store.LoadProfile(p.Name)
	if err != nil {
		t.Fatalf("删完底座档案应当还能读: %v", err)
	}
	if got.HasBase() || got.Domain != "" || got.DomainMode != store.ModeNone {
		t.Fatalf("底座没删干净: %+v", got)
	}

	tunnels, err := store.LoadTunnels(got)
	if err != nil || len(tunnels) != 1 {
		t.Fatalf("独立域名隧道应当原样留着: %v / %+v", err, tunnels)
	}
	// 服务端那行 subDomainHost 要靠重跑脚本去掉，脚本必须还能出得来
	w = call(t, s, "GET", "/api/profiles/"+got.Name+"/deploy-plan", "")
	if w.Code != http.StatusOK {
		t.Fatalf("无底座档案也要能出部署方案，实得 %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "subDomainHost = ") {
		t.Errorf("无底座的新脚本不该再写 subDomainHost:\n%s", w.Body.String())
	}
}

// 删掉底座之后还要能从「新增隧道」里建回来，否则这台服务器就废了一半。
func TestRebuildBaseAfterDelete(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)
	if w := call(t, s, "PUT", "/api/profiles/"+p.Name, `{"domainMode":"none"}`); w.Code != http.StatusOK {
		t.Fatalf("删底座失败: %d %s", w.Code, w.Body.String())
	}

	// 没有底座时挂靠隧道要被明确拒绝，不能写出一条没有地址的 proxy
	if w := call(t, s, "POST", "/api/tunnels", `{"localPort":3000,"subdomain":"web"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("无底座时挂靠隧道应当被拒，实得 %d %s", w.Code, w.Body.String())
	}

	w := call(t, s, "PUT", "/api/profiles/"+p.Name,
		`{"domain":"tunnel.example.com","domainMode":"wildcard"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("建回底座应当成功，实得 %d %s", w.Code, w.Body.String())
	}
	got, _ := store.LoadProfile(p.Name)
	if !got.HasBase() || !got.Wildcard() || got.Domain != "tunnel.example.com" {
		t.Fatalf("底座没建回来: %+v", got)
	}

	if w := call(t, s, "POST", "/api/tunnels", `{"localPort":3000,"subdomain":"web"}`); w.Code != http.StatusOK {
		t.Fatalf("底座建回来后应当能加隧道，实得 %d %s", w.Code, w.Body.String())
	}
}

// 单域名底座只挂得下一条隧道，那条一删它就是个空壳，应当自动收回。
func TestDeleteLastTunnelReleasesSingleBase(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"www.example.com","domainMode":"single"}`)
	if _, err := store.AddTunnel(p, store.TunnelSpec{LocalPort: 3000}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTunnel(p, store.TunnelSpec{
		LocalPort: 8080, CustomDomain: "www.other.com",
	}); err != nil {
		t.Fatal(err)
	}

	if w := call(t, s, "DELETE", "/api/tunnels/3000", ""); w.Code != http.StatusOK {
		t.Fatalf("删隧道失败: %d %s", w.Code, w.Body.String())
	}

	got, err := store.LoadProfile(p.Name)
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if got.HasBase() {
		t.Fatalf("单域名底座空了应当自动收回，实得 %+v", got)
	}
	// 顺带收底座不能把别人的隧道一起收走
	tunnels, err := store.LoadTunnels(got)
	if err != nil || len(tunnels) != 1 || tunnels[0].CustomDomain() != "www.other.com" {
		t.Fatalf("独立域名的隧道被连累了: %v / %+v", err, tunnels)
	}
}

// 删一条独立域名的隧道跟单域名底座毫无关系，底座空着也不该被顺手收走。
//
// 只看「删完之后底座下还有没有隧道」会误判：底座刚建好、还没挂过任何隧道时，
// 这个数本来就是零，删掉一条不相干的独立域名隧道就会把底座一起带走，
// 而删除确认弹窗在那种情况下根本没提示过底座会消失。
func TestDeleteIndependentTunnelKeepsUnusedSingleBase(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"www.example.com","domainMode":"single"}`)
	// 底座建好了但一直空着，只加了一条各走各路的独立域名隧道
	if _, err := store.AddTunnel(p, store.TunnelSpec{
		LocalPort: 8080, CustomDomain: "www.other.com",
	}); err != nil {
		t.Fatal(err)
	}

	if w := call(t, s, "DELETE", "/api/tunnels/8080", ""); w.Code != http.StatusOK {
		t.Fatalf("删隧道失败: %d %s", w.Code, w.Body.String())
	}

	got, err := store.LoadProfile(p.Name)
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if !got.HasBase() || got.Domain != "www.example.com" {
		t.Fatalf("删独立域名隧道不该动到单域名底座，实得 %+v", got)
	}
}

// 泛域名底座空着仍然值钱——解析和证书都配好了，随时能挂新隧道，
// 所以它绝不能跟着最后一条隧道一起消失，只能由用户显式删。
func TestDeleteLastTunnelKeepsWildcardBase(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)
	if _, err := store.AddTunnel(p, store.TunnelSpec{LocalPort: 3000, Subdomain: "web"}); err != nil {
		t.Fatal(err)
	}

	if w := call(t, s, "DELETE", "/api/tunnels/3000", ""); w.Code != http.StatusOK {
		t.Fatalf("删隧道失败: %d %s", w.Code, w.Body.String())
	}
	got, _ := store.LoadProfile(p.Name)
	if !got.HasBase() || got.Domain != "cpolar.example.com" || !got.Wildcard() {
		t.Fatalf("泛域名底座不该被自动收回: %+v", got)
	}
}

// 收回底座失败时，那次补做的重载结果绝不能被吞掉。
//
// 隧道的删除已经落盘了，配置却没重载的话，跑着的 frpc 还在服务这条隧道。
// 此时只报「底座没收回」，用户会以为地址已经失效——真去构造这种双重失败
// 要在文件系统上做两次注入，脆且慢，所以把 apply 做成参数直接测契约本身。
func TestRecoverReleaseFailureReportsBothErrors(t *testing.T) {
	p := store.Profile{Name: "acme", Domain: "www.example.com", DomainMode: store.ModeSingle}
	releaseErr := errors.New("底座收回失败的原因")

	var calls []string
	got := recoverReleaseFailure(p, releaseErr, func(arg store.Profile) error {
		calls = append(calls, arg.Name)
		return errors.New("重载失败的原因")
	})
	if len(calls) != 1 || calls[0] != p.Name {
		t.Fatalf("应当拿原档案补做一次重载，实得 %+v", calls)
	}
	for _, want := range []string{"底座收回失败的原因", "重载失败的原因", "手动重启客户端"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("合并后的消息缺少 %q：%v", want, got)
		}
	}
	if !errors.Is(got, releaseErr) {
		t.Errorf("原始的收回错误应当仍可被 errors.Is 追到：%v", got)
	}

	// 重载成功时不该无中生有地吓唬用户
	got = recoverReleaseFailure(p, releaseErr, func(store.Profile) error { return nil })
	if strings.Contains(got.Error(), "手动重启客户端") {
		t.Errorf("重载成功时不该提示手动重启：%v", got)
	}
}

// 弹窗开着的时候从顶栏切了服务器，这条隧道就会落到另一台机器上。
// 新增隧道时顺带建底座那条路更糟：底座建在 A、隧道落到 B，从现象上完全反推不回来。
func TestCreateTunnelRejectsProfileMismatch(t *testing.T) {
	s := newTestServer(t)
	first := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)

	w := call(t, s, "POST", "/api/tunnels",
		`{"localPort":3000,"subdomain":"web","expectedProfile":"someoneelse"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("目标服务器对不上应当 409，实得 %d %s", w.Code, w.Body.String())
	}
	if tunnels, _ := store.LoadTunnels(first); len(tunnels) != 0 {
		t.Fatalf("被拒的请求不该写进任何档案，实得 %+v", tunnels)
	}

	// 对得上就照常放行，别把正常路径也一并挡了
	if w := call(t, s, "POST", "/api/tunnels",
		`{"localPort":3000,"subdomain":"web","expectedProfile":"`+first.Name+`"}`); w.Code != http.StatusOK {
		t.Fatalf("目标服务器对得上时应当成功，实得 %d %s", w.Code, w.Body.String())
	}
}

// 删底座与给新域名是两件事，同时来说明调用方拼错了，不能猜它想干嘛。
func TestDeleteBaseRejectsDomainAlongside(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)

	w := call(t, s, "PUT", "/api/profiles/"+p.Name,
		`{"domain":"other.example.com","domainMode":"none"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("删底座又给域名应当被拒，实得 %d %s", w.Code, w.Body.String())
	}
	if got, _ := store.LoadProfile(p.Name); got.Domain != "cpolar.example.com" {
		t.Fatalf("被拒后档案不该改动: %+v", got)
	}
}

// ---------- 连通检测的建议 ----------

func checkProfile() store.Profile {
	return store.Profile{
		Name: "visyc", Domain: "cpolar.example.com", DomainMode: "wildcard",
		ServerIP: "1.2.3.4", ServerPort: 7000,
	}
}

// TestAdviceBlamesTokenOnlyWhenRejected 只有服务端真的回绝了才提 token。
func TestAdviceBlamesTokenOnlyWhenRejected(t *testing.T) {
	r := serverCheckResp{
		TCP:          probe.TCPCheck{Result: probe.TCPReachable},
		LoginState:   string(supervisor.StateLoginFailed),
		LoginMessage: "登录被拒绝：token 不一致",
	}
	if got := r.advice(checkProfile()); !strings.Contains(got, "token") {
		t.Fatalf("被拒绝时该指向 token，实得 %q", got)
	}
}

// TestAdviceNeverBlamesTokenWhenNoAnswer 是这套改动要守住的那条线：
// 对端一个字都没回时，把人指向 token 会让他抱着没错的密钥反复重装服务端。
func TestAdviceNeverBlamesTokenWhenNoAnswer(t *testing.T) {
	r := serverCheckResp{
		TCP:          probe.TCPCheck{Result: probe.TCPReachable},
		LoginState:   string(supervisor.StateUnreachable),
		LoginMessage: "连不上 1.2.3.4:7000（connection write timeout）：对端一个字都没回",
	}
	got := r.advice(checkProfile())
	if strings.Contains(got, "token") {
		t.Fatalf("没回音时不该提 token，实得 %q", got)
	}
	if !strings.Contains(got, "connection write timeout") {
		t.Fatalf("该把 frpc 报的真实原因端出来，实得 %q", got)
	}
}

// TestAdviceDoesNotMentionProxy 代理不属于产品诊断范围，旧版 hijacked 响应
// 也不能再把用户引回 VPN 或端口探测。
func TestAdviceDoesNotMentionProxy(t *testing.T) {
	r := serverCheckResp{
		TCP:          probe.TCPCheck{Result: probe.TCPHijacked},
		LoginState:   string(supervisor.StateUnreachable),
		LoginMessage: "连不上 1.2.3.4:7000（connection write timeout）",
	}
	if got := r.advice(checkProfile()); strings.Contains(got, "代理") || strings.Contains(got, "VPN") || strings.Contains(got, "端口探测") {
		t.Fatalf("建议不应再提代理或 VPN，实得 %q", got)
	}
}

// TestAdviceFallsBackWhenNoMessage 监管器没留下话时也得给个方向，
// 不能吐一句空建议。
func TestAdviceFallsBackWhenNoMessage(t *testing.T) {
	r := serverCheckResp{
		TCP:        probe.TCPCheck{Result: probe.TCPReachable},
		LoginState: string(supervisor.StateUnreachable),
	}
	got := r.advice(checkProfile())
	if got == "" || strings.Contains(got, "token") {
		t.Fatalf("兜底建议既不能为空也不能提 token，实得 %q", got)
	}
	if !strings.Contains(got, "1.2.3.4:7000") {
		t.Fatalf("兜底建议该点明连的是哪台，实得 %q", got)
	}
}

// TestAdvicePassesWhenLoggedInAndResolved 正常路径不受影响。
func TestAdvicePassesWhenLoggedInAndResolved(t *testing.T) {
	r := serverCheckResp{
		TCP:        probe.TCPCheck{Result: probe.TCPReachable},
		LoginState: string(supervisor.StateRunning),
		DNS:        probe.DNSCheck{Result: probe.DNSOK},
	}
	if got := r.advice(checkProfile()); !strings.Contains(got, "验收通过") {
		t.Fatalf("登录成功且解析到位应当放行，实得 %q", got)
	}
}

// TestAdviceStillCatchesMissingWildcard 登录成功后该报的解析问题不能被吞掉。
func TestAdviceStillCatchesMissingWildcard(t *testing.T) {
	r := serverCheckResp{
		TCP:        probe.TCPCheck{Result: probe.TCPReachable},
		LoginState: string(supervisor.StateRunning),
		DNS:        probe.DNSCheck{Result: probe.DNSMissing},
	}
	if got := r.advice(checkProfile()); !strings.Contains(got, "泛解析") {
		t.Fatalf("泛域名缺解析该被点出来，实得 %q", got)
	}
}

// TestCheckServerReportsTCPAsTriState 钉住界面依赖的那个字段：① 那一步
// 现在是三态而不是布尔，前端靠 tcp.result 区分「不可信」与「不通」。
// 旧的 tcpOpen 布尔必须彻底消失，留着会让前端悄悄读到 undefined。
func TestCheckServerReportsTCPAsTriState(t *testing.T) {
	s := newTestServer(t)
	mustCreateProfile(t, s,
		`{"serverIp":"127.0.0.1","domain":"cpolar.example.com","domainMode":"wildcard"}`)

	w := call(t, s, "POST", "/api/check/server", "")
	if w.Code != http.StatusOK {
		t.Fatalf("检测应当成功，实得 %d %s", w.Code, w.Body.String())
	}
	var got struct {
		TCP struct {
			Result string `json:"result"`
		} `json:"tcp"`
		TCPOpen *bool `json:"tcpOpen"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	switch got.TCP.Result {
	case "reachable", "unreachable":
	default:
		t.Fatalf("tcp.result 必须是 reachable 或 unreachable，实得 %q", got.TCP.Result)
	}
	if got.TCPOpen != nil {
		t.Fatal("旧的 tcpOpen 布尔应当已经移除")
	}
}

func TestAccessLogPluginHiddenUntilEnabled(t *testing.T) {
	s := newTestServer(t)
	w := call(t, s, "GET", "/api/state", "")
	if w.Code != http.StatusOK {
		t.Fatalf("读取状态失败: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"accessLog":false`) {
		t.Fatalf("插件默认应关闭:\n%s", w.Body.String())
	}

	w = call(t, s, "GET", "/api/plugins/access-log", "")
	if w.Code != http.StatusOK {
		t.Fatalf("读取访问日志插件失败: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("插件状态默认应关闭:\n%s", w.Body.String())
	}
}

func TestAccessLogEnableRewriteAndPerTunnelToggle(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)
	if w := call(t, s, "POST", "/api/tunnels",
		`{"localPort":8888,"subdomain":"web","expectedProfile":"`+p.Name+`"}`); w.Code != http.StatusOK {
		t.Fatalf("加隧道失败: %d %s", w.Code, w.Body.String())
	}

	w := call(t, s, "PUT", "/api/plugins/access-log", `{"enabled":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("开启访问日志失败: %d %s", w.Code, w.Body.String())
	}

	st := call(t, s, "GET", "/api/state", "")
	if !strings.Contains(st.Body.String(), `"accessLog":true`) {
		t.Fatalf("开启后状态应带 accessLog=true:\n%s", st.Body.String())
	}

	conf, err := os.ReadFile(p.ConfPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(conf), "localPort = 8888") {
		t.Fatalf("插件开启后 frpc 不应再直连本机端口:\n%s", conf)
	}
	if !strings.Contains(string(conf), `originPort = "8888"`) {
		t.Fatalf("真实端口应保留在 originPort:\n%s", conf)
	}

	got := call(t, s, "GET", "/api/plugins/access-log", "")
	if !strings.Contains(got.Body.String(), `"logging":true`) {
		t.Fatalf("新建隧道默认应开启记录:\n%s", got.Body.String())
	}

	off := call(t, s, "PUT", "/api/plugins/access-log/tunnels/8888", `{"enabled":false}`)
	if off.Code != http.StatusOK {
		t.Fatalf("关闭单条隧道记录失败: %d %s", off.Code, off.Body.String())
	}
	conf, err = os.ReadFile(p.ConfPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(conf), "localPort = 8888") {
		t.Fatalf("关掉这条记录后应恢复直连本机端口:\n%s", conf)
	}

	if err := store.AppendAccessLog(p.Name, 8888, "line\n"); err != nil {
		t.Fatal(err)
	}
	del := call(t, s, "DELETE", "/api/plugins/access-log/tunnels/8888/log", "")
	if del.Code != http.StatusOK {
		t.Fatalf("删除日志失败: %d %s", del.Code, del.Body.String())
	}
	if _, err := os.Stat(store.AccessLogPath(p.Name, 8888)); !os.IsNotExist(err) {
		t.Fatal("删除接口应当把日志文件去掉")
	}
}

func TestPortSitesPluginHiddenUntilEnabled(t *testing.T) {
	s := newTestServer(t)
	w := call(t, s, "GET", "/api/state", "")
	if w.Code != http.StatusOK {
		t.Fatalf("读取状态失败: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"portSites":false`) {
		t.Fatalf("插件默认应关闭:\n%s", w.Body.String())
	}

	w = call(t, s, "GET", "/api/plugins/port-sites", "")
	if w.Code != http.StatusOK {
		t.Fatalf("读取端口管理插件失败: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("插件状态默认应关闭:\n%s", w.Body.String())
	}
}

func TestPortSitesEnableCreateStartAndDisableStopsListen(t *testing.T) {
	s := newTestServer(t)
	port := freeLocalPort(t)

	on := call(t, s, "PUT", "/api/plugins/port-sites", `{"enabled":true}`)
	if on.Code != http.StatusOK {
		t.Fatalf("开启插件失败: %d %s", on.Code, on.Body.String())
	}
	st := call(t, s, "GET", "/api/state", "")
	if !strings.Contains(st.Body.String(), `"portSites":true`) {
		t.Fatalf("开启后状态应带 portSites=true:\n%s", st.Body.String())
	}

	created := call(t, s, "POST", "/api/plugins/port-sites/sites",
		fmt.Sprintf(`{"port":%d,"start":true}`, port))
	if created.Code != http.StatusOK {
		t.Fatalf("创建并启动失败: %d %s", created.Code, created.Body.String())
	}
	if !strings.Contains(created.Body.String(), `"running":true`) {
		t.Fatalf("启动后应 running=true:\n%s", created.Body.String())
	}

	files := call(t, s, "GET", "/api/plugins/port-sites/sites/"+strconv.Itoa(port)+"/files", "")
	if files.Code != http.StatusOK || !strings.Contains(files.Body.String(), "index.html") {
		t.Fatalf("文件列表应含初始页: %d %s", files.Code, files.Body.String())
	}

	off := call(t, s, "PUT", "/api/plugins/port-sites", `{"enabled":false}`)
	if off.Code != http.StatusOK {
		t.Fatalf("关闭插件失败: %d %s", off.Code, off.Body.String())
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("关闭插件后监听应释放: %v", err)
	}
	_ = ln.Close()

	cfg, err := store.LoadPortSites()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sites) != 1 {
		t.Fatalf("关插件应保留站点配置: %+v", cfg.Sites)
	}
}

func TestPortSitesRejectsTunnelLocalPort(t *testing.T) {
	s := newTestServer(t)
	p := mustCreateProfile(t, s,
		`{"serverIp":"1.2.3.4","domain":"cpolar.example.com","domainMode":"wildcard"}`)
	if w := call(t, s, "POST", "/api/tunnels",
		`{"localPort":8888,"subdomain":"web","expectedProfile":"`+p.Name+`"}`); w.Code != http.StatusOK {
		t.Fatalf("加隧道失败: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, s, "PUT", "/api/plugins/port-sites", `{"enabled":true}`); w.Code != http.StatusOK {
		t.Fatalf("开启插件失败: %d %s", w.Code, w.Body.String())
	}
	w := call(t, s, "POST", "/api/plugins/port-sites/sites", `{"port":8888}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "已被隧道占用") {
		t.Fatalf("隧道端口应冲突: %d %s", w.Code, w.Body.String())
	}
}

func TestPortSitesDeleteFilesFlag(t *testing.T) {
	s := newTestServer(t)
	if w := call(t, s, "PUT", "/api/plugins/port-sites", `{"enabled":true}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	keepPort := freeLocalPort(t)
	if w := call(t, s, "POST", "/api/plugins/port-sites/sites",
		fmt.Sprintf(`{"port":%d}`, keepPort)); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	site, ok, err := store.FindPortSite(keepPort)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if w := call(t, s, "DELETE", "/api/plugins/port-sites/sites/"+strconv.Itoa(keepPort)+"?deleteFiles=false", ""); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(site.Root, "index.html")); err != nil {
		t.Fatalf("不删文件时应保留目录: %v", err)
	}

	dropPort := freeLocalPort(t)
	if w := call(t, s, "POST", "/api/plugins/port-sites/sites",
		fmt.Sprintf(`{"port":%d}`, dropPort)); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	dropped, ok, err := store.FindPortSite(dropPort)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if w := call(t, s, "DELETE", "/api/plugins/port-sites/sites/"+strconv.Itoa(dropPort)+"?deleteFiles=true", ""); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(dropped.Root); !os.IsNotExist(err) {
		t.Fatal("勾选删文件后应去掉托管目录")
	}
}

func TestPortSitesUploadRejectsTraversalName(t *testing.T) {
	s := newTestServer(t)
	port := freeLocalPort(t)
	if w := call(t, s, "PUT", "/api/plugins/port-sites", `{"enabled":true}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := call(t, s, "POST", "/api/plugins/port-sites/sites",
		fmt.Sprintf(`{"port":%d}`, port)); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	site, ok, err := store.FindPortSite(port)
	if err != nil || !ok {
		t.Fatal(err)
	}

	w := callUpload(t, s, "/api/plugins/port-sites/sites/"+strconv.Itoa(port)+"/files", "../escape.txt", "hacked")
	if w.Code != http.StatusOK {
		t.Fatalf("上传应把文件名收成基名并写入站点根: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(site.Root), "escape.txt")); err == nil {
		t.Fatal("上传不得写到站点根目录之外")
	}
	if _, err := os.Stat(filepath.Join(site.Root, "escape.txt")); err != nil {
		t.Fatalf("文件应落在站点根: %v", err)
	}
}

func TestPortSitesRestoreRunningOnNew(t *testing.T) {
	s := newTestServer(t)
	port := freeLocalPort(t)
	if w := call(t, s, "PUT", "/api/plugins/port-sites", `{"enabled":true}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := call(t, s, "POST", "/api/plugins/port-sites/sites",
		fmt.Sprintf(`{"port":%d,"start":true}`, port)); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	_ = s.sites.StopAll()

	s2, err := New(supervisor.New(), 0, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.sites.StopAll() })
	if !s2.sites.Running(port) {
		t.Fatal("面板重启且插件仍开启时，上次运行中的站点应自动拉起")
	}
}

func TestPortSitesPickDirCanceledIsOK(t *testing.T) {
	s := newTestServer(t)
	restore := store.OverridePickDirectory(func() (string, bool, error) {
		return "", true, nil
	})
	t.Cleanup(restore)

	w := call(t, s, "POST", "/api/plugins/port-sites/pick-dir", "{}")
	if w.Code != http.StatusOK {
		t.Fatalf("取消应 200 而不是错误: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"canceled":true`) {
		t.Fatalf("应标记 canceled: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("取消不该带 error: %s", w.Body.String())
	}
}

func TestPortSitesPickDirReturnsPath(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	restore := store.OverridePickDirectory(func() (string, bool, error) {
		return dir, false, nil
	})
	t.Cleanup(restore)

	w := call(t, s, "POST", "/api/plugins/port-sites/pick-dir", "{}")
	if w.Code != http.StatusOK {
		t.Fatalf("选目录失败: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Path     string `json:"path"`
		Canceled bool   `json:"canceled"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Canceled || resp.Path != dir {
		t.Fatalf("应返回所选路径: %+v", resp)
	}
}

func TestPortSitesDeleteFileAndRejectTraversal(t *testing.T) {
	s := newTestServer(t)
	port := freeLocalPort(t)
	if w := call(t, s, "PUT", "/api/plugins/port-sites", `{"enabled":true}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := call(t, s, "POST", "/api/plugins/port-sites/sites",
		fmt.Sprintf(`{"port":%d}`, port)); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	site, ok, err := store.FindPortSite(port)
	if err != nil || !ok {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(site.Root), "secret.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(site.Root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}

	trav := call(t, s, "DELETE", "/api/plugins/port-sites/sites/"+strconv.Itoa(port)+"/files/"+url.PathEscape("../secret.txt"), "")
	if trav.Code != http.StatusBadRequest {
		t.Fatalf("穿越应拒绝: %d %s", trav.Code, trav.Body.String())
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("站外文件不得被删")
	}

	dirDel := call(t, s, "DELETE", "/api/plugins/port-sites/sites/"+strconv.Itoa(port)+"/files/assets", "")
	if dirDel.Code != http.StatusBadRequest || !strings.Contains(dirDel.Body.String(), "文件夹") {
		t.Fatalf("目录删除应拒绝: %d %s", dirDel.Code, dirDel.Body.String())
	}

	gone := call(t, s, "DELETE", "/api/plugins/port-sites/sites/"+strconv.Itoa(port)+"/files/index.html", "")
	if gone.Code != http.StatusOK {
		t.Fatalf("删除文件失败: %d %s", gone.Code, gone.Body.String())
	}
	if strings.Contains(gone.Body.String(), "index.html") {
		t.Fatalf("列表不应再含已删文件: %s", gone.Body.String())
	}
	if _, err := os.Stat(filepath.Join(site.Root, "index.html")); !os.IsNotExist(err) {
		t.Fatal("index.html 应被删掉")
	}
}

func TestPortSitesBrowseSubdirUploadPageAndRejectTraversal(t *testing.T) {
	s := newTestServer(t)
	port := freeLocalPort(t)
	if w := call(t, s, "PUT", "/api/plugins/port-sites", `{"enabled":true}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := call(t, s, "POST", "/api/plugins/port-sites/sites",
		fmt.Sprintf(`{"port":%d}`, port)); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	site, ok, err := store.FindPortSite(port)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(site.Root, "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site.Root, "css", "app.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := "/api/plugins/port-sites/sites/" + strconv.Itoa(port) + "/files"

	trav := call(t, s, "GET", base+"?dir="+url.QueryEscape("../"), "")
	if trav.Code != http.StatusBadRequest {
		t.Fatalf("列上级应拒绝: %d %s", trav.Code, trav.Body.String())
	}

	listed := call(t, s, "GET", base+"?dir=css", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "app.css") {
		t.Fatalf("应列出 css 目录: %d %s", listed.Code, listed.Body.String())
	}
	if !strings.Contains(listed.Body.String(), `"dir":"css"`) {
		t.Fatalf("应回当前 dir: %s", listed.Body.String())
	}

	up := callUpload(t, s, base+"?dir=css", "theme.css", "ok")
	if up.Code != http.StatusOK {
		t.Fatalf("上传到子目录失败: %d %s", up.Code, up.Body.String())
	}
	if _, err := os.Stat(filepath.Join(site.Root, "css", "theme.css")); err != nil {
		t.Fatalf("文件应落在 css 下: %v", err)
	}

	del := call(t, s, "DELETE", base+"/theme.css?dir=css", "")
	if del.Code != http.StatusOK {
		t.Fatalf("删除子目录文件失败: %d %s", del.Code, del.Body.String())
	}
	if _, err := os.Stat(filepath.Join(site.Root, "css", "theme.css")); !os.IsNotExist(err) {
		t.Fatal("css/theme.css 应被删掉")
	}

	paged := call(t, s, "GET", base+"?limit=1&offset=0", "")
	if paged.Code != http.StatusOK {
		t.Fatalf("分页失败: %d %s", paged.Code, paged.Body.String())
	}
	var page struct {
		Files  []store.PortSiteFile `json:"files"`
		Total  int                  `json:"total"`
		Offset int                  `json:"offset"`
		Limit  int                  `json:"limit"`
	}
	if err := json.Unmarshal(paged.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Limit != 1 || page.Offset != 0 || len(page.Files) != 1 || page.Total < 2 {
		t.Fatalf("分页字段不对: %+v body=%s", page, paged.Body.String())
	}
}

func freeLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func callUpload(t *testing.T, s *Server, path, filename, body string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, body); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, &buf)
	r.Host = "127.0.0.1"
	r.Header.Set("Authorization", "Bearer "+s.token)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
