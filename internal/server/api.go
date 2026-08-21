package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openfrees/frp-ngrok/internal/apitypes"
	"github.com/openfrees/frp-ngrok/internal/deploy"
	"github.com/openfrees/frp-ngrok/internal/frpcbin"
	"github.com/openfrees/frp-ngrok/internal/hotkey"
	"github.com/openfrees/frp-ngrok/internal/installer"
	"github.com/openfrees/frp-ngrok/internal/paths"
	"github.com/openfrees/frp-ngrok/internal/probe"
	"github.com/openfrees/frp-ngrok/internal/store"
	"github.com/openfrees/frp-ngrok/internal/supervisor"
)

func toView(p store.Profile, current string) apitypes.Profile {
	return apitypes.Profile{
		Name:       p.Name,
		Domain:     p.Domain,
		DomainMode: store.NormalizeDomainMode(p.DomainMode),
		ServerIP:   p.ServerIP,
		ServerPort: p.ServerPort,
		VhostPort:  p.VhostPort,
		Current:    p.Name == current,
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	currentID := store.CurrentID()
	all := store.LoadAllProfiles()

	resp := apitypes.State{
		Version:     s.version,
		Port:        s.port,
		Autostart:   installer.AutostartEnabled(),
		FrpcVersion: frpcbin.InstalledVersion(),
		Client:      s.sup.Status(),
		Profiles:    make([]apitypes.Profile, 0, len(all)),
		Tunnels:     []apitypes.Tunnel{},
		DataDir:     dataDirDisplay(),
		AccessLog:   accessLogEnabled(),
		PortSites:   portSitesEnabled(),
		Hotkeys:     hotkeysEnabled(),
		Locale:      store.Locale(),
	}
	for _, p := range all {
		resp.Profiles = append(resp.Profiles, toView(p, currentID))
	}

	if cur, err := store.ResolveCurrent(); err == nil {
		v := toView(cur, cur.Name)
		resp.Current = &v
		tunnels, tErr := store.LoadTunnels(cur)
		if tErr == nil {
			for _, t := range tunnels {
				view := apitypes.Tunnel{
					Name:      t.Name,
					LocalPort: t.LocalPort,
					Subdomain: t.Subdomain,
					Host:      t.Host(cur),
					URL:       t.PublicURL(cur),
					LocalUp:   probe.LocalPortInUse(t.LocalPort),
				}
				if t.Independent() {
					view.CustomDomain = t.CustomDomain()
				}
				resp.Tunnels = append(resp.Tunnels, view)
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------- 服务器档案 ----------

type createProfileReq struct {
	ServerIP   string `json:"serverIp"`
	Domain     string `json:"domain"`
	DomainMode string `json:"domainMode"`
	// LocalPort 只用于单域名接入向导：域名与端口一起提交，档案保存后立刻
	// 建好第一条隧道。零值保留旧接口行为，部署页仍可只创建服务器档案。
	LocalPort  int    `json:"localPort"`
	ServerPort int    `json:"serverPort"`
	VhostPort  int    `json:"vhostPort"`
	Token      string `json:"token"`
	// Activate 决定新档案是否立刻接管连接。留空按 true，保持接入向导的行为；
	// 部署页新建服务器时显式传 false，免得把正在跑的隧道切走。
	Activate *bool `json:"activate"`
}

func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var req createProfileReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	mode, err := parseDomainMode(req.DomainMode)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	domain := store.NormalizeDomain(req.Domain)
	if err := checkDomainAgainstMode(req.Domain, mode); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	p := draftProfile(req, mode)
	p.Name = store.SuggestProfileID(domain)
	if p.Token == "" {
		tok, err := randomToken()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		p.Token = tok
	}

	if err := store.SaveProfile(p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := store.EnsureConf(p); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if req.LocalPort != 0 {
		if mode != store.ModeSingle {
			_ = store.DeleteProfile(p.Name)
			writeErr(w, http.StatusBadRequest, fmt.Errorf("只有单域名接入时才能同时填写本机端口"))
			return
		}
		if _, err := store.AddTunnel(p, store.TunnelSpec{LocalPort: req.LocalPort}); err != nil {
			// 新档案还没有调用方可见，首条隧道失败时整份撤回，避免留下一台
			// 看似接入成功、实际没有用户刚填写映射的半成品服务器。
			_ = store.DeleteProfile(p.Name)
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		_ = store.EnsureTunnelLogDefault(p.Name, req.LocalPort)
	}

	// 没有任何档案时必须认领当前位，否则控制台仍是「还没接入服务器」的空状态。
	activate := req.Activate == nil || *req.Activate || store.CurrentID() == ""
	if activate {
		if err := store.SetCurrentID(p.Name); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}

	// 档案已通过 Validate，脚本不该再失败；真失败了也如实报，不吐半份脚本。
	script, err := deploy.Script(p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"profile":   toView(p, store.CurrentID()),
		"script":    script,
		"activated": activate,
	})
}

type updateProfileReq struct {
	ServerIP   string `json:"serverIp"`
	Domain     string `json:"domain"`
	DomainMode string `json:"domainMode"`
	// PreserveSingleDomain 表示从单域名建立泛域名底座时，原单域名已经是
	// 一条真实地址，应改成独立域名继续服务，而不是跟随新底座重算。
	PreserveSingleDomain bool   `json:"preserveSingleDomain"`
	ServerPort           int    `json:"serverPort"`
	Token                string `json:"token"`
}

// handleUpdateProfile 改一台已接入服务器的连接信息、域名与域名模式。
//
// 这是唯一会动既有档案的入口，只能由用户从「编辑服务器」「修改域名」显式发起。
// 字段留空一律表示不改这一项：界面把服务器与域名拆成了两个弹窗，
// 各自只提交自己那几项，不能让没填的字段把对方刚改好的设置冲掉。
//
// 域名一变，frpc.toml 里挂靠它的隧道寻址方式都要跟着重写，
// 所以这里必须连配置一起落盘，再让当前连接带着新配置重连。
func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	p, err := store.LoadProfile(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var req updateProfileReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	before := p
	if err := applyDomainChange(&p, req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	applyServerChange(&p, req)

	tunnels, err := store.LoadTunnels(before)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	preserveSingle := req.PreserveSingleDomain && before.HasBase() && !before.Wildcard() && p.Wildcard()
	if preserveSingle {
		tunnels = store.PreserveSingleDomain(before, tunnels)
	}
	// 档案域名只能指向一处，切过去之前得让用户自己决定留哪条；
	// 绑了独立域名的隧道各有各的地址，不占这个位置。
	base := store.BaseTunnels(tunnels)
	switch {
	case !p.HasBase() && len(base) > 0:
		writeErr(w, http.StatusBadRequest, fmt.Errorf(
			"还有 %d 条隧道挂在 %s 上，底座一删它们就没地址了；先删掉这些隧道，或给它们各绑一个独立域名",
			len(base), before.DisplayDomain()))
		return
	case p.HasBase() && !p.Wildcard() && len(base) > 1:
		writeErr(w, http.StatusBadRequest, fmt.Errorf(
			"当前有 %d 条隧道挂在档案域名上，单域名模式只能留一条，先删到只剩一条、或给多余的那些各绑一个独立域名再切", len(base)))
		return
	}
	// 新底座罩住已有的独立域名时 frps 会拒收，宁可拦在这里也不让隧道静默失效
	if bad, cErr := store.ConflictWithBase(before, p); cErr == nil && len(bad) > 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf(
			"隧道用的独立域名 %s 会落在新底座 *.%s 下面，frps 不收这种域名；先改掉这些隧道的域名，或者换个底座",
			strings.Join(bad, "、"), p.Domain))
		return
	}
	// 换完域名两条隧道可能撞到同一个地址上，先预演一遍再动档案
	if err := store.CheckPlan(p, tunnels); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	if err := store.SaveProfile(p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// 档案已经改了、配置没跟上，是最难查的一种半截状态：回滚档案再报错。
	var migrateErr error
	if preserveSingle {
		migrateErr = store.MigrateTunnels(before, p, tunnels)
	} else {
		migrateErr = store.MigrateConf(before, p)
	}
	if migrateErr != nil {
		if rbErr := store.SaveProfile(before); rbErr != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf(
				"%w（回滚档案也失败: %v，请检查 %s）", migrateErr, rbErr, paths.ProfileMeta(p.Name)))
			return
		}
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("%w（已回滚，档案未改动）", migrateErr))
		return
	}

	if store.CurrentID() != p.Name {
		s.writeAction(w, nil)
		return
	}
	s.writeAction(w, s.sup.Start(p))
}

// applyDomainChange 按请求改域名与域名模式，两项都没填就原样不动。
//
// 只给模式不给域名是改不了的：模式决定域名怎么解释，单独换一个会让
// 「泛域名后缀」和「固定域名」互相串味，隧道地址跟着变成用户没要过的样子。
// 唯一的例外是 domainMode=none —— 删底座本来就不需要域名。
func applyDomainChange(p *store.Profile, req updateProfileReq) error {
	domain := strings.TrimSpace(req.Domain)
	rawMode := strings.TrimSpace(req.DomainMode)
	if domain == "" && rawMode == "" {
		return nil
	}
	if rawMode == store.ModeNone {
		if err := checkDomainAgainstMode(domain, store.ModeNone); err != nil {
			return err
		}
		p.Domain = ""
		p.DomainMode = store.ModeNone
		return nil
	}
	if domain == "" {
		return fmt.Errorf("改域名模式会一并改掉隧道地址，请把域名也填上")
	}

	mode := store.NormalizeDomainMode(p.DomainMode)
	// 从无底座建回底座时，旧模式是 none，不能拿它当默认——那会绕过下面的
	// 合法性分支，把域名按「无底座」解释掉，等于什么也没建成。
	if mode == store.ModeNone {
		mode = store.ModeWildcard
	}
	if rawMode != "" {
		parsed, err := parseDomainMode(rawMode)
		if err != nil {
			return err
		}
		mode = parsed
	}
	if err := checkDomainAgainstMode(domain, mode); err != nil {
		return err
	}
	p.Domain = store.NormalizeDomain(domain)
	p.DomainMode = mode
	return nil
}

// applyServerChange 按请求改连接信息，字段留空或为零表示不改这一项。
// 具体的合法性由 store.Profile.Validate 在落盘时统一把关，这里不重复一遍。
func applyServerChange(p *store.Profile, req updateProfileReq) {
	if ip := strings.TrimSpace(req.ServerIP); ip != "" {
		p.ServerIP = ip
	}
	if req.ServerPort != 0 {
		p.ServerPort = req.ServerPort
	}
	if tok := strings.TrimSpace(req.Token); tok != "" {
		p.Token = tok
	}
}

// draftProfile 把表单参数拼成一份档案，补齐调用方没给的端口默认值。
func draftProfile(req createProfileReq, mode string) store.Profile {
	p := store.Profile{
		Domain:     store.NormalizeDomain(req.Domain),
		DomainMode: mode,
		ServerIP:   strings.TrimSpace(req.ServerIP),
		ServerPort: req.ServerPort,
		VhostPort:  req.VhostPort,
		Token:      strings.TrimSpace(req.Token),
	}
	if p.ServerPort == 0 {
		p.ServerPort = store.DefaultServerPort
	}
	if p.VhostPort == 0 {
		p.VhostPort = store.DefaultVhostPort
	}
	return p
}

// deployPlan 把档案翻译成部署页要的全部内容。path 为空表示脚本还没落盘。
func deployPlan(p store.Profile, path string) (apitypes.DeployPlan, error) {
	script, err := deploy.Script(p)
	if err != nil {
		return apitypes.DeployPlan{}, err
	}
	certNote := "证书用 Let's Encrypt 并选 DNS 验证——通配符证书只能用这种方式签发。签好后打开强制 HTTPS。"
	switch {
	case !p.HasBase():
		certNote = "这台服务器没有底座域名：站点与证书跟着各条隧道的独立域名走，每个域名各建一个站点、各签一张普通证书即可。"
	case !p.Wildcard():
		certNote = "证书用 Let's Encrypt 即可，单域名走默认的 HTTP 验证就能签。签好后打开强制 HTTPS。"
	}
	return apitypes.DeployPlan{
		Script:      script,
		NginxConfig: deploy.NginxConfig(p.VhostPort),
		Path:        path,
		Domain:      p.Domain,
		DomainMode:  store.NormalizeDomainMode(p.DomainMode),
		RootDomain:  store.RootDomain(p.Domain),
		IP:          p.ServerIP,
		Port:        p.ServerPort,
		Vhost:       p.VhostPort,
		Token:       p.Token,
		DNSRecords:  p.DNSRecords(),
		SiteDomains: p.SiteDomains(),
		CertNote:    certNote,
	}, nil
}

// writePlan 统一回写部署方案，生成失败时按 400 报错而不是吐半份脚本。
func (s *Server) writePlan(w http.ResponseWriter, p store.Profile, path string) {
	plan, err := deployPlan(p, path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// parseDomainMode 严格解析接口传入的模式。
//
// store.NormalizeDomainMode 的兜底是给没有该字段的老档案用的；
// 接口这一层必须较真，否则把 mode 拼错成 singel 会静默按泛域名走，
// 用户拿到的是另一套 DNS、证书和 frps 配置。
func parseDomainMode(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case store.ModeSingle:
		return store.ModeSingle, nil
	case store.ModeNone:
		return store.ModeNone, nil
	case store.ModeWildcard, "":
		return store.ModeWildcard, nil
	}
	return "", fmt.Errorf("域名模式只能是 %s、%s 或 %s",
		store.ModeWildcard, store.ModeSingle, store.ModeNone)
}

// checkDomainAgainstMode 拦住「选了单域名却填通配符」这种自相矛盾的输入。
// 静默去掉星号会让用户以为泛解析已经生效，出问题时更难查。
func checkDomainAgainstMode(raw, mode string) error {
	if mode == store.ModeNone {
		if strings.TrimSpace(raw) != "" {
			return fmt.Errorf("要删底座就不能同时给域名；要换底座请直接给新域名和对应的模式")
		}
		return nil
	}
	if mode == store.ModeSingle && strings.HasPrefix(strings.TrimSpace(raw), "*.") {
		return fmt.Errorf("单域名模式不能填 *. 开头的通配符域名；确实要用通配符请切到泛域名模式")
	}
	if !store.ValidDomain(store.NormalizeDomain(raw)) {
		if mode == store.ModeSingle {
			return fmt.Errorf("域名不合法，填一个完整域名，例如 www.example.com")
		}
		return fmt.Errorf("泛域名后缀不合法，例如 cpolar.example.com（不用带 *. 前缀）")
	}
	return nil
}

func (s *Server) handleActivateProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := store.LoadProfile(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := store.SetCurrentID(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// 切换服务器必然要换连接，无论此前是否在跑都直接启动。
	s.writeAction(w, s.sup.Start(p))
}

// writeAction 统一返回操作结果：错误只代表本次未成功，不影响后续自动重试。
func (s *Server) writeAction(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusOK, apitypes.ActionResult{
		OK:      err == nil,
		Message: errText(err),
		Client:  s.sup.Status(),
	})
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == store.CurrentID() {
		s.sup.Stop()
	}
	if err := store.DeleteProfile(id); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	// 删掉的是当前档案时，自动切到剩下的第一台，没有则清空。
	if store.CurrentID() == "" {
		if remaining := store.ListProfileIDs(); len(remaining) > 0 {
			_ = store.SetCurrentID(remaining[0])
			if p, err := store.LoadProfile(remaining[0]); err == nil {
				_ = s.sup.Start(p)
			}
		} else {
			_ = store.ClearCurrent()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 隧道 ----------

type createTunnelReq struct {
	LocalPort int    `json:"localPort"`
	Subdomain string `json:"subdomain"`
	// CustomDomain 有值时这条隧道绑自己的域名，不挂在档案的泛域名底座下。
	CustomDomain string `json:"customDomain"`
	// ExpectedProfile 是调用方以为自己正在操作的那台服务器。
	//
	// 这个接口按「当前档案」落盘，而当前档案随时可能被切走：弹窗开着的时候
	// 从顶栏换一台服务器，隧道就会加到另一台上去；新增隧道时顺带建底座那条路
	// 更糟，底座建在 A、隧道落到 B。前端把自己认定的那台带上来，两边对不上
	// 就当场拒绝——这种错配一旦写进去，从现象上完全反推不回来。
	// 留空表示调用方不关心，按老行为走。
	ExpectedProfile string `json:"expectedProfile"`
}

func (s *Server) handleCreateTunnel(w http.ResponseWriter, r *http.Request) {
	p, err := store.ResolveCurrent()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var req createTunnelReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if want := strings.TrimSpace(req.ExpectedProfile); want != "" && want != p.Name {
		writeErr(w, http.StatusConflict, fmt.Errorf(
			"当前服务器已经切换到 %s，这条隧道本来是要加到 %s 上的；关掉弹窗重新打开一次再填", p.Name, want))
		return
	}
	if _, err := store.AddTunnel(p, store.TunnelSpec{
		LocalPort:    req.LocalPort,
		Subdomain:    strings.TrimSpace(req.Subdomain),
		CustomDomain: strings.TrimSpace(req.CustomDomain),
	}); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	_ = store.EnsureTunnelLogDefault(p.Name, req.LocalPort)
	s.writeAction(w, s.sup.ApplyConfig(p))
}

func (s *Server) handleDeleteTunnel(w http.ResponseWriter, r *http.Request) {
	p, err := store.ResolveCurrent()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	port, convErr := strconv.Atoi(r.PathValue("port"))
	if convErr != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("端口参数不合法"))
		return
	}
	removed, kept, err := store.RemoveTunnel(p, port)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	next, releaseErr := releaseSingleBase(p, removed, kept)
	if releaseErr != nil {
		s.writeAction(w, recoverReleaseFailure(p, releaseErr, s.sup.ApplyConfig))
		return
	}
	s.writeAction(w, s.sup.ApplyConfig(next))
}

// recoverReleaseFailure 处理「隧道删掉了，但空底座没能一并收回」这一步。
//
// 隧道确实删掉了，所以不能报成「删除失败」；但也不能不吭声——不说清楚，
// 用户下次加隧道会撞上一个看不见的占位。更要紧的是那次补做的重载：删除已经
// 落盘，配置没重载的话跑着的 frpc 还在服务这条隧道，只报「底座没收回」
// 会让用户以为地址已经失效了。两个结果必须都出现在同一条消息里。
//
// apply 走参数传进来，是为了让这条错误合并的契约能被直接测到：真去构造
// 「收回失败 + 重载也失败」要在文件系统上做双重注入，脆且慢。
func recoverReleaseFailure(p store.Profile, releaseErr error, apply func(store.Profile) error) error {
	msg := fmt.Errorf("隧道已删除，但单域名底座 %s 没能一并收回：%w", p.Domain, releaseErr)
	if applyErr := apply(p); applyErr != nil {
		return fmt.Errorf("%w；而且客户端没能重载配置（%v），这条隧道可能还在对外服务，请到「设置」页手动重启客户端",
			msg, applyErr)
	}
	return msg
}

// releaseSingleBase 在单域名底座被腾空时把它一并收回，返回收回后的档案。
//
// 单域名底座只能挂一条隧道，那条删掉之后它就是个空壳：既没人用，又让
// 「新增隧道」只剩一个选项（再加一条会被挡在「已经指向本机 N 端口」上）。
// 用户删隧道的意思就是不要这个地址了，留着它反而要求用户再去别处删一次。
//
// 判据直接问「删掉的那条是不是挂在底座上的」，而不是比对前后两份快照：
// 用户删的若是一条独立域名隧道，跟底座毫无关系，把底座收走等于背着用户改
// 档案——界面在那种情况下压根不会提示底座会消失。而两份快照是两次独立读取，
// 中间文件被动过就会推出一个根本没发生过的因果，所以 removed 必须来自
// RemoveTunnel 自己那一次读。
//
// 泛域名底座不参与这套逻辑：它空着仍然值钱——解析和证书都是配好的，随时能
// 挂新隧道，所以那边只保留手动删除。
func releaseSingleBase(p store.Profile, removed store.Tunnel, kept []store.Tunnel) (store.Profile, error) {
	if !p.HasBase() || p.Wildcard() {
		return p, nil
	}
	if removed.Independent() || len(store.BaseTunnels(kept)) > 0 {
		return p, nil
	}
	next := p
	next.Domain = ""
	next.DomainMode = store.ModeNone
	if err := store.SaveProfile(next); err != nil {
		return p, err
	}
	// 与改档案同一条规矩：档案改了而配置没跟上是最难查的半截状态，回滚再报错。
	if err := store.MigrateConf(p, next); err != nil {
		if rbErr := store.SaveProfile(p); rbErr != nil {
			return p, fmt.Errorf("%w（回滚档案也失败: %v，请检查 %s）", err, rbErr, paths.ProfileMeta(p.Name))
		}
		return p, fmt.Errorf("%w（已回滚，底座未改动）", err)
	}
	return next, nil
}

// ---------- 客户端开关 ----------

func (s *Server) handleClientAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	var err error
	switch action {
	case "stop":
		s.sup.Stop()
	case "start", "restart":
		var p store.Profile
		if p, err = store.ResolveCurrent(); err == nil {
			err = s.sup.Start(p)
		}
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("未知操作: %s", action))
		return
	}
	s.writeAction(w, err)
}

func (s *Server) handleAutostart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var err error
	if req.Enabled {
		err = installer.EnableAutostart()
	} else {
		err = installer.DisableAutostart()
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"autostart": installer.AutostartEnabled(),
	})
}

func (s *Server) handleGetPrefs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, store.LoadPrefs())
}

func (s *Server) handlePutPrefs(w http.ResponseWriter, r *http.Request) {
	var req store.Prefs
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := store.SavePrefs(req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, store.LoadPrefs())
}

// handleServiceStop 停止整套本地服务，隧道随之全部断开。
func (s *Server) handleServiceStop(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "本地服务正在停止，隧道已断开。下次双击启动器即可重新开启。",
	})
	// 先把响应发回浏览器，再结束自身进程。
	go func() {
		time.Sleep(300 * time.Millisecond)
		s.sup.Stop()
		if s.sites != nil {
			_ = s.sites.StopAll()
		}
		_ = installer.StopService()
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
}

// ---------- 连通性检测 ----------

type serverCheckResp struct {
	TCP          probe.TCPCheck `json:"tcp"`
	LoginState   string         `json:"loginState"`
	LoginMessage string         `json:"loginMessage"`
	DNS          probe.DNSCheck `json:"dns"`
	Advice       string         `json:"advice"`
}

func (s *Server) handleCheckServer(w http.ResponseWriter, _ *http.Request) {
	p, err := store.ResolveCurrent()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	resp := serverCheckResp{}
	resp.TCP = probe.CheckTCP(p.ServerIP, p.ServerPort, 4*time.Second)

	if resp.TCP.Unreachable() {
		resp.Advice = fmt.Sprintf("%s:%d 连不上：部署脚本没跑、安全组没放行，或 IP 填错了",
			p.ServerIP, p.ServerPort)
		resp.LoginState = string(supervisor.StateStopped)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// 端口通只说明有程序在听，必须以登录结果确认对端是本方案的 frps。
	if st := s.sup.Status(); !st.Running() {
		if startErr := s.sup.Start(p); startErr != nil {
			resp.LoginMessage = startErr.Error()
		}
	}
	st := s.sup.Status()
	resp.LoginState = string(st.State)
	if resp.LoginMessage == "" {
		resp.LoginMessage = st.LastError
	}

	// 隧道实际用的是哪个名字就查哪个：泛域名模式下决定成败的是泛解析，
	// 而不是根记录（根记录只影响站点本身，之前查它会把泛解析缺失judged成通过）。
	// 无底座时压根没有档案解析可查，硬查空主机名只会得到一个吓人的红叉。
	resp.DNS = probe.DNSCheck{Result: probe.DNSSkipped}
	if p.HasBase() {
		resp.DNS = probe.CheckDNS(p.PublicHost("probe"), p.ServerIP, 4*time.Second)
	}

	resp.Advice = resp.advice(p)
	writeJSON(w, http.StatusOK, resp)
}

// advice 按已采到的证据给出下一步。
//
// 状态的分辨力就是建议的准确度：被拒绝说明对端听见了我们，该去核对 token；
// 没回音说明话根本没送到，该去查网络。把两者混成一句「多半是 token 不一致」，
// 就会让人抱着一个没有错的密钥反复重装服务端。
func (r serverCheckResp) advice(p store.Profile) string {
	switch {
	case r.LoginState == string(supervisor.StateLoginFailed):
		return "登录被服务端拒绝：多半是 token 不一致，或那台机器上跑的不是本方案的 frps。重新复制部署脚本到服务器跑一遍。"
	case r.LoginState != string(supervisor.StateRunning):
		msg := r.LoginMessage
		if msg == "" {
			msg = fmt.Sprintf("连不上 %s:%d：先看服务器安全组，再看那台机器上 frps 有没有在跑",
				p.ServerIP, p.ServerPort)
		}
		return msg
	case !p.HasBase():
		return "服务器验收通过。这台服务器没有底座域名，各条隧道的解析与证书跟着它们各自的独立域名走，到「连通检测」下半段逐条看。"
	case !r.DNS.OK() && p.Wildcard():
		return "已连上服务器，但泛解析 *." + p.Domain + " 还没指到这台机器。" +
			"去 DNS 后台把泛解析那条 A 记录指向 " + p.ServerIP
	case !r.DNS.OK():
		return "已连上服务器，但 " + p.Domain + " 还没解析到这台机器。去 DNS 后台把它的 A 记录指向 " + p.ServerIP
	}
	return "服务器验收通过，可以开始加隧道了。"
}

type tunnelCheck struct {
	Subdomain string          `json:"subdomain"`
	LocalPort int             `json:"localPort"`
	URL       string          `json:"url"`
	DNS       probe.DNSCheck  `json:"dns"`
	HTTP      probe.HTTPCheck `json:"http"`
	LocalUp   bool            `json:"localUp"`
	OK        bool            `json:"ok"`
	Advice    string          `json:"advice"`
}

func (s *Server) handleCheckTunnels(w http.ResponseWriter, _ *http.Request) {
	p, err := store.ResolveCurrent()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	tunnels, err := store.LoadTunnels(p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	results := make([]tunnelCheck, len(tunnels))
	var wg sync.WaitGroup
	for i, t := range tunnels {
		wg.Add(1)
		go func(i int, t store.Tunnel) {
			defer wg.Done()
			host := t.Host(p)
			c := tunnelCheck{
				Subdomain: t.Subdomain,
				LocalPort: t.LocalPort,
				URL:       t.PublicURL(p),
				LocalUp:   probe.LocalPortInUse(t.LocalPort),
			}
			c.DNS = probe.CheckDNS(host, p.ServerIP, 4*time.Second)
			if c.DNS.OK() {
				c.HTTP = probe.CheckHTTP(c.URL, 12*time.Second)
			}
			c.Advice = probe.Explain(c.DNS, c.HTTP, p.ServerIP, c.LocalUp)
			c.OK = c.DNS.OK() && c.HTTP.Reachable() && c.HTTP.StatusCode < 500
			results[i] = c
		}(i, t)
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// ---------- 部署脚本与日志 ----------

func (s *Server) handleDeployScript(w http.ResponseWriter, _ *http.Request) {
	p, err := store.ResolveCurrent()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	path, err := deploy.Export(p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.writePlan(w, p, path)
}

// handleProfileDeployPlan 出指定档案的部署方案，与当前连的是哪台无关。
//
// 部署页要能查任意一台已接入服务器，包括当前没在用的那些，所以不能沿用
// handleDeployScript 的 ResolveCurrent。
//
// 这里必须读已落盘的档案，绝不能按请求参数现拼一份：那样 token 只能临时生成，
// 而用户改了密钥后正是来这里重新取脚本的，拿一个对不上的 token 去服务器重跑，
// 两端就永远登录失败。
func (s *Server) handleProfileDeployPlan(w http.ResponseWriter, r *http.Request) {
	p, err := store.LoadProfile(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	path, err := deploy.Export(p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.writePlan(w, p, path)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	p, err := store.ResolveCurrent()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	lines := 200
	if n, convErr := strconv.Atoi(r.URL.Query().Get("lines")); convErr == nil && n > 0 && n <= 2000 {
		lines = n
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"log":  supervisor.TailLog(p.LogPath(), lines),
		"path": p.LogPath(),
	})
}

// ---------- 插件 · 命令行工具快捷键 ----------

func hotkeysEnabled() bool {
	cfg, err := store.LoadHotkeys()
	return err == nil && cfg.Enabled
}

// handleGetHotkeys 返回快捷键插件的配置与平台能力。
func (s *Server) handleGetHotkeys(w http.ResponseWriter, _ *http.Request) {
	cfg, err := store.LoadHotkeys()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	paletteCombo := cfg.PaletteCombo
	if strings.TrimSpace(paletteCombo) == "" {
		paletteCombo = store.DefaultPaletteCombo
	}
	if normalized, err := store.NormalizeHotkeyCombo(paletteCombo); err == nil {
		paletteCombo = normalized
	}
	writeJSON(w, http.StatusOK, apitypes.HotkeysState{
		Enabled:      cfg.Enabled,
		Items:        cfg.Items,
		OrderVersion: cfg.OrderVersion,
		PaletteCombo: paletteCombo,
		Supported:    s.hotkeys.Supported(),
	})
}

// handlePutHotkeys 整体替换快捷键配置：校验、注册、再落盘。
//
// 顺序是先注册后落盘——注册失败时旧配置还在盘上、旧注册也恢复原样，
// 界面与实际状态始终对得上，不会出现「盘里说开着、系统里没在听」的假开启。
func (s *Server) handlePutHotkeys(w http.ResponseWriter, r *http.Request) {
	var cfg store.HotkeyConfig
	if err := readJSON(r, &cfg); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := store.ValidateHotkeys(&cfg); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	old, loadErr := store.LoadHotkeys()
	if loadErr != nil {
		old = store.HotkeyConfig{}
	}
	if err := s.hotkeys.Apply(cfg); err != nil {
		_ = s.hotkeys.Apply(old) // 尽力恢复旧注册
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := store.SaveHotkeys(cfg); err != nil {
		_ = s.hotkeys.Apply(old)
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRunHotkey 从控制台试跑一条命令，返回结果文字供用户验证。
func (s *Server) handleRunHotkey(w http.ResponseWriter, r *http.Request) {
	var item store.HotkeyItem
	if err := readJSON(r, &item); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if item.Command == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("命令不能为空"))
		return
	}
	msg, err := hotkey.Test(item)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
}

// ---------- 插件 · 访问日志 ----------

func accessLogEnabled() bool {
	cfg, err := store.LoadAccessLog()
	return err == nil && cfg.Enabled
}

func (s *Server) handleGetAccessLog(w http.ResponseWriter, _ *http.Request) {
	cfg, err := store.LoadAccessLog()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	resp := apitypes.AccessLogState{Enabled: cfg.Enabled, Tunnels: []apitypes.AccessLogTunnel{}}
	p, err := store.ResolveCurrent()
	if err != nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	tunnels, err := store.LoadTunnels(p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, t := range tunnels {
		size, _ := store.AccessLogSize(p.Name, t.LocalPort)
		resp.Tunnels = append(resp.Tunnels, apitypes.AccessLogTunnel{
			LocalPort: t.LocalPort,
			Host:      t.Host(p),
			URL:       t.PublicURL(p),
			Logging:   store.TunnelLogEnabled(cfg, p.Name, t.LocalPort),
			Size:      size,
			SizeText:  store.FormatSize(size),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

type putAccessLogReq struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) handlePutAccessLog(w http.ResponseWriter, r *http.Request) {
	var req putAccessLogReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := store.LoadAccessLog()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	cfg.Enabled = req.Enabled
	if err := store.SaveAccessLog(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.ensureAccessProxy(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.writeAction(w, s.rewriteAccessLogConf())
}

type putAccessLogTunnelReq struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) handlePutAccessLogTunnel(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port <= 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("端口参数不合法"))
		return
	}
	p, err := store.ResolveCurrent()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !tunnelExists(p, port) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("没找到端口 %d 的隧道", port))
		return
	}
	var req putAccessLogTunnelReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := store.SetTunnelLogEnabled(p.Name, port, req.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.ensureAccessProxy(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.writeAction(w, s.rewriteAccessLogConf())
}

func (s *Server) handleGetAccessLogFile(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port <= 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("端口参数不合法"))
		return
	}
	p, err := store.ResolveCurrent()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !tunnelExists(p, port) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("没找到端口 %d 的隧道", port))
		return
	}
	lines := 300
	if n, convErr := strconv.Atoi(r.URL.Query().Get("lines")); convErr == nil && n > 0 && n <= 2000 {
		lines = n
	}
	path := store.AccessLogPath(p.Name, port)
	writeJSON(w, http.StatusOK, map[string]any{
		"log":  supervisor.TailLog(path, lines),
		"path": path,
	})
}

func (s *Server) handleDeleteAccessLogFile(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port <= 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("端口参数不合法"))
		return
	}
	p, err := store.ResolveCurrent()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !tunnelExists(p, port) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("没找到端口 %d 的隧道", port))
		return
	}
	if err := store.DeleteAccessLog(p.Name, port); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func tunnelExists(p store.Profile, port int) bool {
	tunnels, err := store.LoadTunnels(p)
	if err != nil {
		return false
	}
	for _, t := range tunnels {
		if t.LocalPort == port {
			return true
		}
	}
	return false
}

// ensureAccessProxy 按配置启动或关掉拦截器，并把实际监听端口写回配置。
func (s *Server) ensureAccessProxy() error {
	cfg, err := store.LoadAccessLog()
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return s.access.Stop()
	}
	port, err := s.access.Start(cfg.ListenPort)
	if err != nil {
		return err
	}
	if port == cfg.ListenPort {
		return nil
	}
	cfg.ListenPort = port
	return store.SaveAccessLog(cfg)
}

// rewriteAccessLogConf 按当前开关重写 frpc 的 localPort 并重载客户端。
func (s *Server) rewriteAccessLogConf() error {
	p, err := store.ResolveCurrent()
	if err != nil {
		return nil
	}
	tunnels, err := store.LoadTunnels(p)
	if err != nil {
		return err
	}
	if err := store.SaveTunnels(p, tunnels); err != nil {
		return err
	}
	return s.sup.ApplyConfig(p)
}

// syncAccessLog 启动时恢复拦截器；rewrite 为 true 时同时重写 frpc 配置。
func (s *Server) syncAccessLog(rewrite bool) error {
	if err := s.ensureAccessProxy(); err != nil {
		return err
	}
	if !rewrite {
		return nil
	}
	return s.rewriteAccessLogConf()
}

// ---------- 工具 ----------

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// dataDirDisplay 用 ~ 缩写家目录，避免在界面上暴露完整用户名路径。
func dataDirDisplay() string {
	dir := paths.DataDir()
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(dir, home) {
		return "~" + strings.TrimPrefix(dir, home)
	}
	return dir
}
