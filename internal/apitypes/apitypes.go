// Package apitypes 定义控制台接口的数据结构。
//
// 服务端与菜单栏客户端共用同一份定义，字段改名时编译期即可发现，
// 不会出现一端改了另一端静默失效的情况。
package apitypes

import (
	"github.com/openfrees/frp-ngrok/internal/store"
	"github.com/openfrees/frp-ngrok/internal/supervisor"
)

// Profile 是一台服务器档案的对外视图（不含连接密钥）。
type Profile struct {
	Name string `json:"name"`
	// Domain 不带 *. 前缀，界面上要不要加星号看 DomainMode；无底座时为空。
	Domain     string `json:"domain"`
	DomainMode string `json:"domainMode"`
	ServerIP   string `json:"serverIp"`
	ServerPort int    `json:"serverPort"`
	VhostPort  int    `json:"vhostPort"`
	Current    bool   `json:"current"`
}

// HasBase 判断这台服务器还有没有档案域名（底座）。
func (p Profile) HasBase() bool {
	return store.NormalizeDomainMode(p.DomainMode) != store.ModeNone && p.Domain != ""
}

// DisplayDomain 是界面上代表这台服务器的域名文案。
func (p Profile) DisplayDomain() string {
	if !p.HasBase() {
		return store.T(store.NoBaseLabel, "未设底座")
	}
	if store.NormalizeDomainMode(p.DomainMode) == store.ModeWildcard {
		return "*." + p.Domain
	}
	return p.Domain
}

// DeployPlan 是服务端部署页要展示的全部内容。
//
// 解析记录、站点域名、证书说法都由后端按域名模式算好下发，
// 前端只负责渲染——这套规则只能有一份，不能两端各写一遍。
type DeployPlan struct {
	Script      string `json:"script"`
	NginxConfig string `json:"nginxConfig"`
	// Path 是脚本落盘位置；预览尚未建档案时为空。
	Path       string `json:"path"`
	Domain     string `json:"domain"`
	DomainMode string `json:"domainMode"`
	RootDomain string `json:"rootDomain"`
	IP         string `json:"ip"`
	Port       int    `json:"port"`
	Vhost      int    `json:"vhost"`
	// Token 是脚本里写死的连接密钥。预览时前端要把它原样带回创建接口，
	// 否则用户照预览脚本部署完，新建档案却换了一把钥匙，客户端登录不上。
	Token       string            `json:"token"`
	DNSRecords  []store.DNSRecord `json:"dnsRecords"`
	SiteDomains []string          `json:"siteDomains"`
	CertNote    string            `json:"certNote"`
}

// Tunnel 是一条隧道的对外视图。
type Tunnel struct {
	Name      string `json:"name"`
	LocalPort int    `json:"localPort"`
	Subdomain string `json:"subdomain"`
	// CustomDomain 有值表示这条隧道绑的是自己的域名，界面据此提示解析与证书。
	CustomDomain string `json:"customDomain"`
	// Host 是最终对外的主机名，subdomain 与独立域名两种写法在这里收口。
	Host    string `json:"host"`
	URL     string `json:"url"`
	LocalUp bool   `json:"localUp"`
}

// State 是控制台的整体状态快照。
type State struct {
	Version     string            `json:"version"`
	Port        int               `json:"port"`
	Autostart   bool              `json:"autostart"`
	FrpcVersion string            `json:"frpcVersion"`
	Profiles    []Profile         `json:"profiles"`
	Current     *Profile          `json:"current"`
	Tunnels     []Tunnel          `json:"tunnels"`
	Client      supervisor.Status `json:"client"`
	DataDir     string            `json:"dataDir"`
	// AccessLog 为 true 时，隧道列表和日志页才露出访问日志入口。
	AccessLog bool `json:"accessLog"`
	// PortSites 为 true 时，主导航才露出「端口管理」Tab。
	PortSites bool `json:"portSites"`
	// Hotkeys 为 true 时，菜单栏插件开关显示为已开启。
	Hotkeys bool `json:"hotkeys"`
	// Locale 是控制台界面语言，en 或 zh-CN；新安装默认为 en。
	Locale string `json:"locale"`
}

// ActionResult 是各类操作接口的通用返回。
type ActionResult struct {
	OK      bool              `json:"ok"`
	Message string            `json:"message"`
	Client  supervisor.Status `json:"client"`
}

// HotkeysState 是「命令行工具快捷键」插件的对外状态。
type HotkeysState struct {
	Enabled      bool               `json:"enabled"`
	Items        []store.HotkeyItem `json:"items"`
	OrderVersion int                `json:"orderVersion"`
	PaletteCombo string             `json:"paletteCombo"`
	// Supported 表示当前平台能否注册全局快捷键。
	Supported bool `json:"supported"`
}

// AccessLogState 是「访问日志」插件的对外状态。
type AccessLogState struct {
	Enabled bool              `json:"enabled"`
	Tunnels []AccessLogTunnel `json:"tunnels"`
}

// PortSitesState 是「本地端口管理」插件的对外状态。
type PortSitesState struct {
	Enabled bool           `json:"enabled"`
	Sites   []PortSiteView `json:"sites"`
}

// PortSiteView 是端口管理页上的一张站点卡。
type PortSiteView struct {
	Port               int    `json:"port"`
	Root               string `json:"root"`
	CustomRoot         bool   `json:"customRoot"`
	Running            bool   `json:"running"`
	URL                string `json:"url"`
	Managed            bool   `json:"managed"`
	DeleteFilesDefault bool   `json:"deleteFilesDefault"`
}

// AccessLogTunnel 是设置弹窗里一条隧道的日志状态。
type AccessLogTunnel struct {
	LocalPort int    `json:"localPort"`
	Host      string `json:"host"`
	URL       string `json:"url"`
	Logging   bool   `json:"logging"`
	Size      int64  `json:"size"`
	SizeText  string `json:"sizeText"`
}

// Ping 是启动器与后台服务之间的握手信息，不含敏感内容。
type Ping struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	// BinaryStamp 是服务启动时所用程序文件的修改时间。
	// 启动器据此判断磁盘上的程序是否已更新、需要重启服务换上新代码。
	BinaryStamp int64 `json:"binaryStamp"`
}
