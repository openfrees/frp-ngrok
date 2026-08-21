package store

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/openfrees/frp-ngrok/internal/paths"
)

const (
	// ownerKey 与 ownerValue 组成写在每条 proxy 上的所有权标记。
	//
	// 光看寻址字段认不出这条 proxy 是谁写的：用户手写的 proxy 也可能只有
	// customDomains + localPort。整份重写会抹掉手写配置里的高级字段，
	// 所以必须有个显式标记，宁可漏认自己的，也不能错认别人的。
	ownerKey   = "managedBy"
	ownerValue = "frpanel"

	// bindKey 记录这条隧道的地址是怎么来的。
	//
	// 单域名模式下基础隧道也写 customDomains，与用户显式绑的独立域名长得一模一样；
	// 不把绑定方式记下来，换档案域名时就分不清哪条该跟着变、哪条该原地不动。
	bindKey    = "bind"
	bindBase   = "base"
	bindDomain = "domain"
	// originPortKey 记下这条隧道真正的本机端口。
	// 访问日志插件开启时，写给 frpc 的 localPort 会改成拦截端口；
	// 不把原端口另存一份，界面就会把拦截端口当成用户的服务端口。
	originPortKey = "originPort"
)

// Tunnel 描述一条 HTTP 隧道：本机端口 ↔ 一个公网主机名。
type Tunnel struct {
	Name      string `json:"name" toml:"name"`
	Type      string `json:"type" toml:"type"`
	LocalIP   string `json:"localIp" toml:"localIP"`
	LocalPort int    `json:"localPort" toml:"localPort"`
	// Subdomain 由 frps 拼上它的 subDomainHost，只在挂靠泛域名底座时有值。
	Subdomain string `json:"subdomain" toml:"subdomain"`
	// CustomDomains 把一个完整域名直接绑到这条隧道上，与 Subdomain 二选一。
	// 面板只写一个，多的那些是手工加的，不归面板管。
	CustomDomains []string `json:"customDomains" toml:"customDomains"`
	// Metadatas 存 frp 的 proxy 元数据，面板只用其中的所有权标记。
	Metadatas map[string]string `json:"-" toml:"metadatas"`
}

// CustomDomain 返回这条隧道 customDomains 里的第一个域名。
func (t Tunnel) CustomDomain() string {
	if len(t.CustomDomains) == 0 {
		return ""
	}
	return strings.TrimSpace(t.CustomDomains[0])
}

// originLocalPort 取出用户填的本机端口。配置里的 localPort 可能是拦截端口。
//
// 访问日志插件会把 frpc 的 localPort 改成拦截器端口，真实端口另存在 originPort。
// 若某次重写时还没记下 originPort，下一次保存会把拦截端口写进 originPort，
// 界面就只看得到 50622 这种临时端口。这里要能从 proxy 名把真实端口找回来。
func (t Tunnel) originLocalPort() int {
	origin := metadataOriginPort(t)
	named := portFromProxyName(t.Name)
	listen := accessLogListenPort()
	poisoned := origin > 0 && (origin == listen || (origin == t.LocalPort && named > 0 && named != origin))
	if origin > 0 && !poisoned {
		return origin
	}
	if named > 0 && named != listen {
		return named
	}
	if t.LocalPort > 0 && t.LocalPort != listen {
		return t.LocalPort
	}
	if origin > 0 {
		return origin
	}
	return t.LocalPort
}

func metadataOriginPort(t Tunnel) int {
	if t.Metadatas == nil {
		return 0
	}
	return parsePort(t.Metadatas[originPortKey])
}

// portFromProxyName 从面板生成的 proxy 名还原本机端口。
// local9999 → 9999；sitewww-shop-com-8080 → 8080。
// localweb 这种三级域名不是数字，还原不出来，返回 0。
func portFromProxyName(name string) int {
	switch {
	case strings.HasPrefix(name, "local"):
		return parsePort(name[len("local"):])
	case strings.HasPrefix(name, "site"):
		i := strings.LastIndex(name, "-")
		if i < 0 {
			return 0
		}
		return parsePort(name[i+1:])
	}
	return 0
}

func parsePort(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 || n > 65535 {
		return 0
	}
	return n
}

func accessLogListenPort() int {
	cfg, err := LoadAccessLog()
	if err != nil || !cfg.Enabled || cfg.ListenPort <= 0 {
		return 0
	}
	return cfg.ListenPort
}

var (
	frpcPortMu     sync.RWMutex
	frpcPortMapper func(Profile, Tunnel) int
)

// SetFrpcPortMapper 设置写 frpc.toml 时对 localPort 的改写。
// 访问日志插件用它把开启记录的隧道指到本机拦截器；传 nil 即恢复直连。
func SetFrpcPortMapper(fn func(Profile, Tunnel) int) {
	frpcPortMu.Lock()
	defer frpcPortMu.Unlock()
	frpcPortMapper = fn
}

func mappedFrpcPort(p Profile, t Tunnel) int {
	frpcPortMu.RLock()
	fn := frpcPortMapper
	frpcPortMu.RUnlock()
	if fn == nil {
		return t.LocalPort
	}
	if n := fn(p, t); n > 0 {
		return n
	}
	return t.LocalPort
}

// Independent 判断这条隧道是否绑了自己的独立域名，不随档案域名变动。
//
// 没有标记的是加这个字段之前写出的配置，一律按挂靠档案域名处理，语义不变。
func (t Tunnel) Independent() bool {
	return t.Metadatas[bindKey] == bindDomain && t.CustomDomain() != ""
}

// Host 返回该隧道对外的主机名。独立域名优先，其次才按档案的域名模式推。
func (t Tunnel) Host(p Profile) string {
	if t.Independent() {
		return t.CustomDomain()
	}
	return p.PublicHost(t.Subdomain)
}

// PublicURL 返回该隧道对外的访问地址。
func (t Tunnel) PublicURL(p Profile) string {
	return "https://" + t.Host(p) + "/"
}

// frpcFile 只声明我们关心的字段，其余键由 toml 解析时忽略。
type frpcFile struct {
	Proxies []Tunnel `toml:"proxies"`
}

// managedBy 判断一条 proxy 是否由本面板按这份档案写出来的。
//
// SaveTunnels 是整份重写，只认得 name/type/localIP/localPort 和两种寻址字段；
// 把手工写的 proxy（带 locations、hostHeaderRewrite 之类）也当成自己的，
// 重写时会把那些字段悄悄抹掉。宁可不认，也不能改坏别人手写的配置。
//
// 现在写出去的每条 proxy 都带所有权标记，认领只看标记。没有标记的按加标记
// 之前的两种写法兜底：挂底座的写 subdomain 且不写 customDomains，
// 单域名的写恰好一个、且必然等于本档案域名的 customDomains。
// 兜底判据不能放宽到「任意 customDomains」——那正好是手写 proxy 的样子。
func (t Tunnel) managedBy(p Profile) bool {
	if t.LocalPort == 0 {
		return false
	}
	if t.Metadatas[ownerKey] == ownerValue {
		return true
	}
	if t.Subdomain != "" {
		return len(t.CustomDomains) == 0
	}
	// 没有底座就没有「等于档案域名」这个兜底判据，此时只认所有权标记。
	// 拿空的 p.Domain 去比对，会把手写的 customDomains = [""] 错认成自己的。
	return p.HasBase() && len(t.CustomDomains) == 1 && t.CustomDomains[0] == p.Domain
}

// hasForeignProxyFields 判断配置里是否出现了 Tunnel 不认识的 proxy 字段。
//
// BurntSushi 对数组表只报到 proxies.<字段> 这一层、不带下标，
// 所以定位不到具体是哪一条，只能作为「这份配置被手工动过」的信号。
func hasForeignProxyFields(md toml.MetaData) bool {
	for _, key := range md.Undecoded() {
		if len(key) >= 2 && key[0] == "proxies" {
			return true
		}
	}
	return false
}

// LoadTunnels 读取档案下的全部隧道。配置不存在时返回空列表而非报错。
func LoadTunnels(p Profile) ([]Tunnel, error) {
	data, err := os.ReadFile(p.ConfPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f frpcFile
	if _, err := toml.Decode(string(data), &f); err != nil {
		return nil, fmt.Errorf("解析 frpc.toml 失败: %w", err)
	}
	out := make([]Tunnel, 0, len(f.Proxies))
	for _, t := range f.Proxies {
		if !t.managedBy(p) {
			continue
		}
		t.LocalPort = t.originLocalPort()
		out = append(out, t)
	}
	return out, nil
}

// ForeignProxies 统计配置里不由本面板管理的 proxy 条数。
//
// 这些条目在整份重写时会被丢掉，落盘前得先让用户知道。
func ForeignProxies(p Profile) (int, error) {
	data, err := os.ReadFile(p.ConfPath())
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var f frpcFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range f.Proxies {
		if !t.managedBy(p) {
			n++
		}
	}
	// 有面板不认识的字段，说明这份配置被手工加过料，即便条目本身像面板写的也要留底
	if n == 0 && hasForeignProxyFields(md) {
		n = 1
	}
	return n, nil
}

// SaveTunnels 依据档案与隧道列表重写整份 frpc.toml。
//
// 整体重写而非局部改写，配置永远只有一个真源，不会出现半截残留。
// 代价是这份文件里不归面板管的 proxy 会被抹掉，所以动手前先备份、先校验。
func SaveTunnels(p Profile, tunnels []Tunnel) error {
	planned := NormalizeTunnels(p, tunnels)
	if err := checkPlan(p, planned); err != nil {
		return err
	}
	if err := backupForeign(p); err != nil {
		return err
	}

	addressing := "https://<subdomain>." + p.Domain
	switch {
	case !p.HasBase():
		addressing = "无底座；每条隧道各按自己绑的独立域名走"
	case !p.Wildcard():
		addressing = "https://" + p.Domain + "（单域名模式；另绑独立域名的隧道各按自己的域名走）"
	}

	var b strings.Builder
	fmt.Fprintf(&b, `serverAddr = %q
serverPort = %d

auth.method = "token"
auth.token = %q

log.to = %q
log.level = "info"
log.maxDays = 7

loginFailExit = false
transport.dialServerTimeout = 10
transport.heartbeatInterval = 30
transport.heartbeatTimeout = 90

# 预先建好工作连接，新请求不必等服务端现场要一条，省掉一次往返。
# 开发服务器（Vite 等）单页会发几百个模块请求，这里收益最明显。
transport.poolCount = 10

# ==================== 隧道列表 · 档案 %s ====================
# 公网地址 = %s
# 由隧道管理台自动生成，手工修改可能在下次保存时被覆盖。
`, p.ServerIP, p.ServerPort, p.Token, p.LogPath(), p.Name, addressing)

	for _, t := range planned {
		fmt.Fprintf(&b, `
[[proxies]]
name = %q
type = %q
localIP = %q
localPort = %d
`, t.Name, t.Type, t.LocalIP, mappedFrpcPort(p, t))
		// 寻址方式跟着隧道自己走：绑了独立域名的那条，不受档案模式影响。
		bind := bindBase
		if d := t.CustomDomain(); d != "" {
			fmt.Fprintf(&b, "customDomains = [%q]\n", d)
			if t.Independent() {
				bind = bindDomain
			}
		} else {
			fmt.Fprintf(&b, "subdomain = %q\n", t.Subdomain)
		}
		fmt.Fprintf(&b, "metadatas = { %s = %q, %s = %q, %s = %q }\n",
			ownerKey, ownerValue, bindKey, bind, originPortKey, strconv.Itoa(t.LocalPort))
	}

	if err := os.MkdirAll(paths.ProfileDir(p.Name), 0o755); err != nil {
		return err
	}
	// 配置内含 token，限制为仅当前用户可读。
	return writeFileAtomic(p.ConfPath(), []byte(b.String()), 0o600)
}

// CheckPlan 预演一遍：这批隧道按新档案重算地址之后还站得住吗。
//
// 给改档案的接口用——先问一句再落盘，用户得到的是一条说明白的拒绝，
// 而不是「档案改了、配置写不进去、再回滚」这种要用户猜的半截过程。
func CheckPlan(p Profile, tunnels []Tunnel) error {
	return checkPlan(p, NormalizeTunnels(p, tunnels))
}

// checkPlan 校验即将落盘的这批隧道彼此不冲突、且都还有地址可用。
//
// 换域名、换模式会重算每条隧道的地址，两条隧道很可能在重算之后撞到一起：
// frps 只认第一条注册的 proxy，frpc 遇到重名也只启动第一条，
// 两种情况都是「界面上有两条、实际只有一条能用」，不当场报错就再也查不出来。
func checkPlan(p Profile, tunnels []Tunnel) error {
	// 底座没了，挂靠它的隧道就没有地址了。这种 proxy 写出去 frps 会以
	// 「subdomain is not supported」拒收，界面上却还列着一条，只有翻日志才看得见。
	if !p.HasBase() {
		for _, t := range tunnels {
			if t.CustomDomain() == "" {
				return fmt.Errorf(
					"本机 %d 端口的隧道还挂在档案域名上，而这台服务器已经没有底座了；"+
						"给它绑一个独立域名，或者先把底座建回来", t.LocalPort)
			}
		}
	}

	hosts := make(map[string]int, len(tunnels))
	names := make(map[string]int, len(tunnels))
	for _, t := range tunnels {
		// 这里的地址已由 NormalizeTunnels 算定，档案模式不再影响结果
		host := t.CustomDomain()
		if host == "" {
			host = t.Subdomain
		}
		if port, dup := hosts[host]; dup {
			return fmt.Errorf("本机 %d 和 %d 端口的隧道会指向同一个地址 %s，先改掉其中一条",
				port, t.LocalPort, host)
		}
		if port, dup := names[t.Name]; dup {
			return fmt.Errorf("本机 %d 和 %d 端口的隧道会生成同名的 proxy %s，先改掉其中一条",
				port, t.LocalPort, t.Name)
		}
		hosts[host] = t.LocalPort
		names[t.Name] = t.LocalPort
	}
	return nil
}

// backupForeign 在整份重写之前，把含有手工 proxy 的配置原样抄一份走。
//
// 增删隧道同样是整份重写，从前只有换域名那条路径备份，
// 用户在面板里点一下「新增隧道」就能把手写的 proxy 弄丢，且无处可捡。
func backupForeign(p Profile) error {
	n, err := ForeignProxies(p)
	if err != nil {
		// 数不清有没有手工 proxy 就别写：宁可这次存不上，也不能赌它没有
		return fmt.Errorf("检查手工配置失败，已中止重写: %w", err)
	}
	if n == 0 {
		return nil
	}
	if err := copyAside(p.ConfPath(), p.ConfPath()+".manual.bak"); err != nil {
		return fmt.Errorf("备份手工配置失败，已中止重写: %w", err)
	}
	return nil
}

// NormalizeTunnels 把隧道的寻址方式对齐到档案当前的域名模式。
//
// 切换模式后挂靠底座的老隧道要么缺 subdomain、要么缺 customDomains，
// 不补齐就会写出一份 frps 认不出来的配置，隧道静默失效。
// 绑了独立域名的隧道不参与这套对齐——它的地址本就与档案模式无关。
func NormalizeTunnels(p Profile, tunnels []Tunnel) []Tunnel {
	out := make([]Tunnel, 0, len(tunnels))
	for _, t := range tunnels {
		t.Type = orDefault(t.Type, "http")
		t.LocalIP = orDefault(t.LocalIP, "127.0.0.1")
		if t.Independent() {
			out = append(out, normalizeIndependent(p, t))
			continue
		}
		switch {
		case !p.HasBase():
			// 没有底座可挂，这条隧道就是没地址，原样留着交给 checkPlan 报出来。
			// 绝不能顺手替它编一个域名：编出来的地址 frps 不认，用户也没要过。
			t.Name = orDefault(t.Name, fmt.Sprintf("local%d", t.LocalPort))
			t.CustomDomains = nil
		case p.Wildcard():
			if t.Subdomain == "" {
				t.Subdomain = subdomainFromName(t.Name, t.LocalPort)
			}
			t.CustomDomains = nil
			t.Name = "local" + t.Subdomain
		default:
			// 单域名放不下三级域名，但 name 得留着：切回泛域名时靠它还原原来的地址
			t.Name = orDefault(t.Name, fmt.Sprintf("local%d", t.LocalPort))
			t.Subdomain = ""
			t.CustomDomains = []string{p.Domain}
		}
		out = append(out, t)
	}
	return out
}

// normalizeIndependent 处理绑了独立域名的隧道。
//
// 正常情况原样保留。只有当新底座把这个域名罩进去时才必须动它——
// frps 会拒收底座下面的自定义域名，改写成同地址的三级域名是唯一无损的出路；
// 多级域名没有等价写法，只能留着让上层去拦，绝不静默丢掉用户的地址。
func normalizeIndependent(p Profile, t Tunnel) Tunnel {
	d := t.CustomDomain()
	if sub := strings.TrimSuffix(d, "."+p.Domain); boundToBase(p, d) && ValidSubdomain(sub) {
		t.CustomDomains = nil
		t.Subdomain = sub
		t.Metadatas = nil
		t.Name = "local" + sub
		return t
	}
	t.CustomDomains = []string{d}
	t.Subdomain = ""
	t.Name = customProxyName(d, t.LocalPort)
	return t
}

// customProxyName 由域名和端口拼出 proxy 名。
//
// 只按域名拼是不行的：点换成连字符之后 a.b.com 和 a-b.com 会撞成同一个名字，
// 而 frpc 遇到重名只会启动排在前面的那条、另一条一声不响地消失，
// 连 frpc verify 都查不出来。端口在面板内唯一，带上它名字才唯一。
func customProxyName(domain string, localPort int) string {
	return fmt.Sprintf("site%s-%d", strings.NewReplacer(".", "-", "*", "").Replace(domain), localPort)
}

// boundToBase 判断域名是否落在本档案的泛域名底座下面。
//
// frps 会拒收这种自定义域名（v0.70.1 validateDomainConfigForServer），
// 底座下面的地址只能走 subdomain。
func boundToBase(p Profile, domain string) bool {
	return p.Wildcard() && strings.HasSuffix(domain, "."+p.Domain)
}

// ValidateCustomDomain 校验一个域名能不能作为独立域名绑到这台服务器的隧道上。
func ValidateCustomDomain(p Profile, domain string) error {
	if !ValidDomain(domain) {
		return fmt.Errorf("域名不合法，填一个完整域名，例如 app.example.com")
	}
	if !boundToBase(p, domain) {
		return nil
	}
	sub := strings.TrimSuffix(domain, "."+p.Domain)
	if ValidSubdomain(sub) {
		return fmt.Errorf("%s 就在泛域名底座 *.%s 下面，改用三级域名 %s 即可，证书和解析都是现成的",
			domain, p.Domain, sub)
	}
	return fmt.Errorf(
		"%s 是底座 *.%s 下面的多级域名：frps 不收底座下面的自定义域名，三级域名又不能带点，"+
			"通配符证书也只覆盖一级。换一个不在 %s 下面的域名，或者把底座改小",
		domain, p.Domain, p.Domain)
}

// subdomainFromName 从 name 还原三级域名，还原不出来才退回端口号。
//
// 泛域名 → 单域名 → 泛域名 来回切时，subdomain 字段中途会被清空；
// 不从 name 找回来，用户的 web.example.com 就会变成 3000.example.com，
// 收藏夹和别人手里的链接全断。
func subdomainFromName(name string, localPort int) string {
	if sub := strings.TrimPrefix(name, "local"); sub != name && ValidSubdomain(sub) {
		return sub
	}
	return fmt.Sprint(localPort)
}

// TunnelSpec 描述要新增的一条隧道。
//
// Subdomain 与 CustomDomain 二选一：填了 CustomDomain 就是绑独立域名，
// 否则挂在档案的泛域名底座（或单域名）上。
type TunnelSpec struct {
	LocalPort    int
	Subdomain    string
	CustomDomain string
}

// AddTunnel 追加一条隧道。本机端口与最终公网地址都不可与已有的重复。
func AddTunnel(p Profile, spec TunnelSpec) ([]Tunnel, error) {
	if spec.LocalPort <= 0 || spec.LocalPort > 65535 {
		return nil, fmt.Errorf("端口必须是 1-65535 的数字")
	}
	if err := rejectAccessLogListenPort(spec.LocalPort); err != nil {
		return nil, err
	}

	tunnels, err := LoadTunnels(p)
	if err != nil {
		return nil, err
	}

	fresh := Tunnel{
		Type:      "http",
		LocalIP:   "127.0.0.1",
		LocalPort: spec.LocalPort,
	}

	switch {
	case strings.TrimSpace(spec.CustomDomain) != "":
		domain := NormalizeDomain(spec.CustomDomain)
		if err := ValidateCustomDomain(p, domain); err != nil {
			return nil, err
		}
		fresh.CustomDomains = []string{domain}
		fresh.Metadatas = map[string]string{ownerKey: ownerValue, bindKey: bindDomain}
	case !p.HasBase():
		return nil, fmt.Errorf(
			"这台服务器还没有底座域名，隧道没地方挂：给它绑一个独立域名，或者先建一个泛域名底座")
	case p.Wildcard():
		sub := strings.TrimSpace(spec.Subdomain)
		if sub == "" {
			sub = fmt.Sprint(spec.LocalPort)
		}
		if !ValidSubdomain(sub) {
			return nil, fmt.Errorf("三级域名不合法：不能带点、不能有特殊字符、不能以连字符开头")
		}
		fresh.Subdomain = sub
	default:
		// 单域名模式下这条隧道只能落在档案域名上，没有第二个位置可放。
		if occupied := findByHost(p, tunnels, p.Domain); occupied != nil {
			return nil, fmt.Errorf(
				"%s 已经指向本机 %d 端口了。要再开一条，给它单独绑一个域名，"+
					"或者到「设置」页服务器列表点「域名」改用泛域名",
				p.Domain, occupied.LocalPort)
		}
	}

	host := fresh.Host(p)
	for _, t := range tunnels {
		if t.LocalPort == spec.LocalPort {
			return nil, fmt.Errorf("端口 %d 已有隧道，先删掉再加", spec.LocalPort)
		}
		if t.Host(p) == host {
			return nil, fmt.Errorf("%s 已被本机 %d 端口的隧道占用，换一个地址或先删掉旧的", host, t.LocalPort)
		}
	}

	tunnels = NormalizeTunnels(p, append(tunnels, fresh))
	if err := SaveTunnels(p, tunnels); err != nil {
		return nil, err
	}
	return tunnels, nil
}

// findByHost 找出占用了某个公网地址的隧道。
func findByHost(p Profile, tunnels []Tunnel, host string) *Tunnel {
	for i, t := range tunnels {
		if t.Host(p) == host {
			return &tunnels[i]
		}
	}
	return nil
}

// ConflictWithBase 返回切换到新档案后，会与新泛域名底座冲突的独立域名。
//
// frps 拒收底座下面的自定义域名，这种隧道换完底座会静默失效，
// 所以要在改档案之前就把话说清楚，而不是等用户去日志里找。
func ConflictWithBase(from, to Profile) ([]string, error) {
	tunnels, err := LoadTunnels(from)
	if err != nil {
		return nil, err
	}
	var bad []string
	for _, t := range tunnels {
		d := t.CustomDomain()
		// 罩进底座后还能改写成同地址三级域名的，不算冲突，交给写盘时无损转换
		if !t.Independent() || !boundToBase(to, d) {
			continue
		}
		if ValidSubdomain(strings.TrimSuffix(d, "."+to.Domain)) {
			continue
		}
		bad = append(bad, d)
	}
	return bad, nil
}

// BaseTunnels 返回挂靠在档案域名（底座或单域名）上的隧道，独立域名的不算。
func BaseTunnels(tunnels []Tunnel) []Tunnel {
	out := make([]Tunnel, 0, len(tunnels))
	for _, t := range tunnels {
		if !t.Independent() {
			out = append(out, t)
		}
	}
	return out
}

// PreserveSingleDomain 把单域名档案下的基础隧道改成独立域名隧道。
//
// 用户先用 pan.example.com 接入，再建立 *.tunnel.example.com 底座时，
// pan.example.com 已经是一条对外地址，不是可以丢弃的档案标签。迁移前显式
// 记成独立绑定，后续 NormalizeTunnels 才会让它留在原地址，而不是按新底座
// 重算成 3000.tunnel.example.com。只处理挂靠底座的隧道，原有独立域名不动。
func PreserveSingleDomain(p Profile, tunnels []Tunnel) []Tunnel {
	if !p.HasBase() || p.Wildcard() {
		return tunnels
	}
	out := make([]Tunnel, len(tunnels))
	copy(out, tunnels)
	for i := range out {
		if out[i].Independent() {
			continue
		}
		out[i].CustomDomains = []string{p.Domain}
		out[i].Subdomain = ""
		out[i].Metadatas = map[string]string{ownerKey: ownerValue, bindKey: bindDomain}
	}
	return out
}

// RemoveTunnel 按本机端口删除隧道，返回被删掉的那条和剩下的那些。
//
// 被删的那条也要返回：调用方常常需要据此判断「删掉的是哪一类」。
// 让它自己再 LoadTunnels 一次去比对是不行的——那是另一个快照，
// 两份快照之间文件被动过，就会推出一个根本没发生过的因果。
func RemoveTunnel(p Profile, localPort int) (Tunnel, []Tunnel, error) {
	tunnels, err := LoadTunnels(p)
	if err != nil {
		return Tunnel{}, nil, err
	}
	kept := make([]Tunnel, 0, len(tunnels))
	var removed Tunnel
	found := false
	for _, t := range tunnels {
		if t.LocalPort == localPort {
			removed = t
			found = true
			continue
		}
		kept = append(kept, t)
	}
	if !found {
		return Tunnel{}, nil, fmt.Errorf("没找到端口 %d 的隧道", localPort)
	}
	if err := SaveTunnels(p, kept); err != nil {
		return Tunnel{}, nil, err
	}
	_ = RemoveTunnelAccessLog(p.Name, localPort)
	return removed, kept, nil
}

// EnsureConf 确保档案的 frpc.toml 存在且连接信息为最新，同时保留已有隧道。
func EnsureConf(p Profile) error { return MigrateConf(p, p) }

// MigrateConf 用旧档案的口径读隧道，再按新档案的口径写回去。
//
// 域名或模式一变，隧道的认领方式也跟着变：单域名是靠 customDomains 等于
// 本档案域名来认的，改完域名再去读就全对不上、隧道会凭空消失。
// 所以「读」必须用改之前那份档案，这件事只在这里做一次，调用方不要自己拼。
func MigrateConf(from, to Profile) error {
	tunnels, err := LoadTunnels(from)
	if err != nil {
		// 配置损坏时不静默丢弃用户数据，留一份备份再重建。
		if backupErr := backupBroken(from.ConfPath()); backupErr != nil {
			return fmt.Errorf("%w（备份原配置也失败: %v）", err, backupErr)
		}
		tunnels = nil
	}
	// SaveTunnels 也会备份，但它只能按新档案的口径认手工 proxy：
	// 换域名时有些 proxy 只有对着旧档案才看得出是手工写的，这一趟不能省。
	if n, cErr := ForeignProxies(from); cErr == nil && n > 0 {
		if bErr := copyAside(from.ConfPath(), from.ConfPath()+".manual.bak"); bErr != nil {
			return fmt.Errorf("备份手工配置失败，已中止重写: %w", bErr)
		}
	}
	return SaveTunnels(to, tunnels)
}

// MigrateTunnels 与 MigrateConf 相同，但调用方已经对旧隧道做过显式的寻址转换。
// 仍由这里统一保存，保证手工配置备份、计划校验与原子写盘规则不被绕过。
func MigrateTunnels(from, to Profile, tunnels []Tunnel) error {
	if n, err := ForeignProxies(from); err == nil && n > 0 {
		if copyErr := copyAside(from.ConfPath(), from.ConfPath()+".manual.bak"); copyErr != nil {
			return fmt.Errorf("备份手工配置失败，已中止重写: %w", copyErr)
		}
	}
	return SaveTunnels(to, tunnels)
}

func backupBroken(path string) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	return os.Rename(path, path+".broken.bak")
}

func copyAside(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeFileAtomic(dst, data, 0o600)
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func rejectAccessLogListenPort(port int) error {
	cfg, err := LoadAccessLog()
	if err != nil || !cfg.Enabled || cfg.ListenPort <= 0 {
		return nil
	}
	if port == cfg.ListenPort {
		return fmt.Errorf("端口 %d 正被访问日志拦截器占用，换一个本机端口", port)
	}
	return nil
}
