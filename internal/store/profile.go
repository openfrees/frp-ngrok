// Package store 负责服务器档案与隧道配置的读写。
//
// 磁盘格式与旧版「隧道管理.command」保持一致，两者可交替使用同一份数据。
package store

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

const (
	// DefaultServerPort 是 frps 的控制端口。
	DefaultServerPort = 7000
	// DefaultVhostPort 是 frps 的 vhost 端口，仅监听服务器本机由 nginx 反代。
	DefaultVhostPort = 18080

	// ModeWildcard 是泛域名模式：一张通配符证书撑起任意多个三级域名。
	ModeWildcard = "wildcard"
	// ModeSingle 是单域名模式：整台服务器只对外暴露一个固定域名。
	ModeSingle = "single"
	// ModeNone 是无底座：这台服务器不占任何档案域名，隧道各自绑独立域名。
	//
	// 底座是全服务器唯一的（frps 的 subDomainHost 只有一个值），删掉之后
	// 档案就该真的没有域名，而不是留一个用不上的字符串在那里骗人。
	ModeNone = "none"

	// NoBaseLabel 是无底座时代替域名显示的英文文案；中文走 T()。
	NoBaseLabel = "No base domain"
)

var (
	profileIDRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)
	subdomainRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)
	domainRe    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$`)
	// 令牌会被写进以 root 执行的部署脚本和两端的 toml，只放行无歧义的字符。
	tokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
)

// ValidServerHost 判断服务器地址是否是一个 IP 或合法域名。
//
// 这个值会进 frps 部署脚本（root 执行）、frpc.toml 和 meta.conf，
// 放行任意字符串等于把命令注入和配置注入的口子留在最底层。
func ValidServerHost(h string) bool {
	h = strings.TrimSpace(h)
	if h == "" || len(h) > 253 {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	return domainRe.MatchString(h)
}

// ValidToken 判断连接密钥是否只含安全字符。
func ValidToken(t string) bool { return tokenRe.MatchString(t) }

// Profile 描述一台 frps 服务器的接入信息。
type Profile struct {
	Name string `json:"name"`
	// Domain 一律不带 *. 前缀。泛域名模式下它是通配符的后缀（隧道挂在它下面），
	// 单域名模式下它就是唯一那个对外域名，无底座模式下为空。
	Domain     string `json:"domain"`
	DomainMode string `json:"domainMode"`
	ServerIP   string `json:"serverIp"`
	ServerPort int    `json:"serverPort"`
	Token      string `json:"token"`
	VhostPort  int    `json:"vhostPort"`
}

// DNSRecord 是部署指引里要用户去域名服务商后台加的一条解析。
type DNSRecord struct {
	// Host 是「主机记录」列该填的值，根域用 @。
	Host string `json:"host"`
	Type string `json:"type"`
	// Value 是记录值，这里恒为服务器公网 IP。
	Value string `json:"value"`
	// FQDN 是这条记录最终生效的完整域名，主机记录猜错时用户可据此核对。
	FQDN string `json:"fqdn"`
}

// Wildcard 判断该档案是否走泛域名模式。
func (p Profile) Wildcard() bool { return NormalizeDomainMode(p.DomainMode) == ModeWildcard }

// HasBase 判断这台服务器还有没有档案域名（底座）。
//
// 底座被删掉后档案照常存在，只是所有隧道都得各自绑独立域名。
// 凡是要读 p.Domain 拼地址、出解析记录、出证书说明的地方，都得先问这一句，
// 否则拼出来的会是 "api." 或者空主机名这种一看就废、却不报错的地址。
func (p Profile) HasBase() bool {
	return NormalizeDomainMode(p.DomainMode) != ModeNone && p.Domain != ""
}

// PublicHost 返回一条隧道对外的主机名。
// 泛域名模式拼三级域名，单域名模式下所有隧道都落在同一个域名上。
// 无底座时返回空串——这台服务器没有能挂靠的地址，调用方必须自己兜住。
func (p Profile) PublicHost(subdomain string) string {
	if !p.HasBase() {
		return ""
	}
	if p.Wildcard() && subdomain != "" {
		return subdomain + "." + p.Domain
	}
	return p.Domain
}

// DisplayDomain 是界面上代表这台服务器的域名文案。
func (p Profile) DisplayDomain() string {
	if !p.HasBase() {
		return T(NoBaseLabel, "未设底座")
	}
	if p.Wildcard() {
		return "*." + p.Domain
	}
	return p.Domain
}

// SiteDomains 返回服务器上那个站点需要绑定、且证书需要覆盖的域名。
// 无底座时为空：站点与证书都跟着各条隧道的独立域名走，档案这边没有要求。
func (p Profile) SiteDomains() []string {
	if !p.HasBase() {
		return nil
	}
	if p.Wildcard() {
		return []string{p.Domain, "*." + p.Domain}
	}
	return []string{p.Domain}
}

// DNSRecords 返回需要添加的 A 记录。泛域名模式必须多一条泛解析，否则三级域名全都解析不到。
func (p Profile) DNSRecords() []DNSRecord {
	if !p.HasBase() {
		return nil
	}
	host := HostRecord(p.Domain)
	out := []DNSRecord{{Host: host, Type: "A", Value: p.ServerIP, FQDN: p.Domain}}
	if !p.Wildcard() {
		return out
	}
	wild := "*"
	if host != "@" {
		wild = "*." + host
	}
	return append(out, DNSRecord{Host: wild, Type: "A", Value: p.ServerIP, FQDN: "*." + p.Domain})
}

// NormalizeDomainMode 把外部传入的模式收敛到三个合法值。
// 空值按泛域名处理：这是加模式字段之前的唯一行为，老档案读出来不能变语义。
func NormalizeDomainMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case ModeSingle:
		return ModeSingle
	case ModeNone:
		return ModeNone
	}
	return ModeWildcard
}

// NormalizeDomain 去掉用户顺手粘进来的 *. 前缀和首尾的点，统一大小写。
func NormalizeDomain(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	d = strings.TrimPrefix(d, "*.")
	d = strings.TrimPrefix(d, ".")
	return strings.TrimSuffix(d, ".")
}

// multiLabelSuffixes 收录常见的两段式公共后缀。
//
// 只用来把域名切成「主机记录 + 根域」给用户抄，不做安全判断，
// 因此不必引入完整的公共后缀表；万一猜错，DNS 表里并排的完整域名仍然是对的。
var multiLabelSuffixes = map[string]bool{
	"com.cn": true, "net.cn": true, "org.cn": true, "gov.cn": true, "edu.cn": true, "ac.cn": true,
	"com.hk": true, "com.tw": true, "com.sg": true, "com.my": true,
	"co.uk": true, "org.uk": true, "me.uk": true,
	"co.jp": true, "ne.jp": true, "or.jp": true, "co.kr": true,
	"com.au": true, "net.au": true, "org.au": true, "co.nz": true, "com.br": true,
}

// RootDomain 推测注册域，例如 a.b.example.com → example.com、x.example.com.cn → example.com.cn。
func RootDomain(domain string) string {
	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return domain
	}
	if len(parts) >= 3 && multiLabelSuffixes[parts[len(parts)-2]+"."+parts[len(parts)-1]] {
		return strings.Join(parts[len(parts)-3:], ".")
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// HostRecord 返回域名相对根域的主机记录；域名本身就是根域时返回 @。
func HostRecord(domain string) string {
	root := RootDomain(domain)
	if domain == root {
		return "@"
	}
	return strings.TrimSuffix(domain, "."+root)
}

// ConfPath 返回该档案的 frpc 配置文件路径。
func (p Profile) ConfPath() string { return paths.ProfileConf(p.Name) }

// LogPath 返回该档案的 frpc 日志路径。
func (p Profile) LogPath() string { return paths.ProfileLog(p.Name) }

// Validate 校验档案字段是否可用于生成配置。
func (p Profile) Validate() error {
	if !ValidProfileID(p.Name) {
		return fmt.Errorf("档案名不合法：只能用字母、数字、下划线、连字符，且不能以连字符开头")
	}
	if strings.TrimSpace(p.ServerIP) == "" {
		return fmt.Errorf("服务器地址不能为空")
	}
	if !ValidServerHost(p.ServerIP) {
		return fmt.Errorf("服务器地址不合法，填 IP 或域名，例如 1.2.3.4")
	}
	switch NormalizeDomainMode(p.DomainMode) {
	case ModeNone:
		// 无底座就该真的没有域名：留一个读不到、改不了的残值下来，
		// 界面显示「未设底座」而配置里却写着旧域名，排查时无从对账。
		if p.Domain != "" {
			return fmt.Errorf("无底座模式下不能留着域名 %s，删底座时要一并清空", p.Domain)
		}
	case ModeSingle:
		if !ValidDomain(p.Domain) {
			return fmt.Errorf("域名不合法，填一个完整域名，例如 www.example.com")
		}
	default:
		if !ValidDomain(p.Domain) {
			return fmt.Errorf("泛域名后缀不合法，例如 cpolar.example.com（不用带 *. 前缀）")
		}
	}
	if strings.TrimSpace(p.Token) == "" {
		return fmt.Errorf("连接密钥不能为空")
	}
	if !ValidToken(p.Token) {
		return fmt.Errorf("连接密钥只能用字母、数字、下划线、连字符，长度 8-128")
	}
	if p.ServerPort <= 0 || p.ServerPort > 65535 {
		return fmt.Errorf("服务端口必须在 1-65535 之间")
	}
	if p.VhostPort <= 0 || p.VhostPort > 65535 {
		return fmt.Errorf("vhost 端口必须在 1-65535 之间")
	}
	return nil
}

// ValidProfileID 判断档案名是否合法。
func ValidProfileID(id string) bool { return profileIDRe.MatchString(id) }

// ValidSubdomain 判断三级域名前缀是否合法（不含点、不以连字符开头）。
func ValidSubdomain(s string) bool { return subdomainRe.MatchString(s) }

// ValidDomain 判断泛域名后缀是否形如 a.b 或 a.b.c。
func ValidDomain(d string) bool {
	return strings.Contains(d, ".") && domainRe.MatchString(d)
}

// ListProfileIDs 返回全部档案名，按字母序排列。
func ListProfileIDs() []string {
	entries, err := os.ReadDir(paths.ProfilesDir())
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(paths.ProfileMeta(e.Name())); err != nil {
			continue
		}
		ids = append(ids, e.Name())
	}
	sort.Strings(ids)
	return ids
}

// LoadProfile 读取指定档案。
func LoadProfile(id string) (Profile, error) {
	var p Profile
	if !ValidProfileID(id) {
		return p, fmt.Errorf("档案名不合法: %s", id)
	}
	f, err := os.Open(paths.ProfileMeta(id))
	if err != nil {
		return p, fmt.Errorf("档案不存在: %s", id)
	}
	defer f.Close()

	kv := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if err := sc.Err(); err != nil {
		return p, err
	}

	p = Profile{
		Name:       id,
		Domain:     kv["domain"],
		DomainMode: NormalizeDomainMode(kv["domain_mode"]),
		ServerIP:   kv["server_ip"],
		Token:      kv["token"],
		ServerPort: atoiOr(kv["server_port"], DefaultServerPort),
		VhostPort:  atoiOr(kv["vhost_port"], DefaultVhostPort),
	}
	if p.ServerIP == "" || p.Token == "" {
		return p, fmt.Errorf("档案 %s 不完整（需要 server_ip / token）", id)
	}
	// 「有没有底座」这件事，模式和域名必须说的是同一句话，两个方向都要查。
	// 只查一半的话，读进来的档案会处在 Validate 明令拒绝的状态：界面按模式
	// 显示「未设底座」，meta.conf 里却还写着个域名，下次保存直接失败，
	// 用户得到的是一份看得见、改不动、也说不清哪儿不对的档案。
	if p.Domain == "" && p.DomainMode != ModeNone {
		return p, fmt.Errorf("档案 %s 不完整（%s 模式缺 domain；没有底座请写 domain_mode=%s）",
			id, p.DomainMode, ModeNone)
	}
	if p.Domain != "" && p.DomainMode == ModeNone {
		return p, fmt.Errorf("档案 %s 自相矛盾（domain_mode=%s 却留着 domain=%s；要么删掉这行 domain，要么把模式改回 %s）",
			id, ModeNone, p.Domain, ModeWildcard)
	}
	return p, nil
}

// LoadAllProfiles 读取全部可用档案，跳过损坏项。
func LoadAllProfiles() []Profile {
	var out []Profile
	for _, id := range ListProfileIDs() {
		if p, err := LoadProfile(id); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// SaveProfile 写入档案信息。
func SaveProfile(p Profile) error {
	p.DomainMode = NormalizeDomainMode(p.DomainMode)
	if err := p.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.ProfileDir(p.Name), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf(`# frp 服务器档案 · 由隧道管理台生成，勿手改格式
name=%s
domain=%s
domain_mode=%s
server_ip=%s
server_port=%d
token=%s
vhost_port=%d
`, p.Name, p.Domain, p.DomainMode, p.ServerIP, p.ServerPort, p.Token, p.VhostPort)
	// token 属于敏感凭据，只对当前用户可读。
	return writeFileAtomic(paths.ProfileMeta(p.Name), []byte(body), 0o600)
}

// DeleteProfile 删除档案目录（只删本地配置，不影响远端 frps）。
func DeleteProfile(id string) error {
	if !ValidProfileID(id) {
		return fmt.Errorf("档案名不合法: %s", id)
	}
	dir := paths.ProfileDir(id)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("档案不存在: %s", id)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("删除失败: %w", err)
	}
	if err := RemoveProfileAccessLogs(id); err != nil {
		return fmt.Errorf("档案已删除，但访问日志没能清掉: %w", err)
	}
	return nil
}

// CurrentID 返回当前启用的档案名；无有效档案时返回空串。
func CurrentID() string {
	b, err := os.ReadFile(paths.CurrentFile())
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(b))
	if id == "" || !ValidProfileID(id) {
		return ""
	}
	if _, err := os.Stat(paths.ProfileMeta(id)); err != nil {
		return ""
	}
	return id
}

// SetCurrentID 设置当前启用的档案。
func SetCurrentID(id string) error {
	if err := os.MkdirAll(paths.ProfilesDir(), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(paths.CurrentFile(), []byte(id+"\n"), 0o644)
}

// ClearCurrent 清除当前档案标记。
func ClearCurrent() error {
	err := os.Remove(paths.CurrentFile())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ResolveCurrent 返回当前档案；若未设置则自动选第一个可用档案。
func ResolveCurrent() (Profile, error) {
	id := CurrentID()
	if id == "" {
		ids := ListProfileIDs()
		if len(ids) == 0 {
			return Profile{}, fmt.Errorf("还没有配置任何服务器")
		}
		id = ids[0]
		_ = SetCurrentID(id)
	}
	return LoadProfile(id)
}

// SuggestProfileID 依据域名推导档案名，撞名时自动加序号。
// 例如 cpolar.example.com 推出 example。
func SuggestProfileID(domain string) string {
	parts := strings.Split(domain, ".")
	base := "server"
	if len(parts) >= 2 {
		base = parts[len(parts)-2]
	}
	if !ValidProfileID(base) {
		base = "server"
	}
	id := base
	for n := 2; ; n++ {
		if _, err := os.Stat(paths.ProfileMeta(id)); os.IsNotExist(err) {
			return id
		}
		id = fmt.Sprintf("%s%d", base, n)
	}
}

func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// writeFileAtomic 先写临时文件再原子改名，避免写到一半崩溃留下半截配置。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
