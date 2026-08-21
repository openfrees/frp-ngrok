//go:build darwin && cgo

package tray

import (
	"fmt"
	"log"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/openfrees/frp-ngrok/internal/apitypes"
	"github.com/openfrees/frp-ngrok/internal/client"
	"github.com/openfrees/frp-ngrok/internal/installer"
	"github.com/openfrees/frp-ngrok/internal/store"
)

// Supported 表示当前构建是否包含菜单栏能力。
func Supported() bool { return true }

const (
	// systray 不支持移除菜单项，隧道行用固定池子按需显示隐藏。
	tunnelSlots  = 8
	pollInterval = 3 * time.Second
)

// Run 驻留菜单栏并阻塞，直到用户选择退出。
//
// 必须在主协程调用：macOS 要求界面操作发生在主线程。
func Run(c *client.Client) {
	writePID()
	m := &menu{cli: c}
	systray.Run(m.onReady, removePID)
}

type menu struct {
	cli *client.Client

	status  *systray.MenuItem
	server  *systray.MenuItem
	open    *systray.MenuItem
	tunnels []*systray.MenuItem
	more    *systray.MenuItem
	toggle  *systray.MenuItem
	restart *systray.MenuItem
	lang    *systray.MenuItem
	langEN  *systray.MenuItem
	langZH  *systray.MenuItem
	plugins *systray.MenuItem
	plugHK  *systray.MenuItem
	plugAL  *systray.MenuItem
	plugPS  *systray.MenuItem
	quit    *systray.MenuItem

	mu    sync.Mutex
	state apitypes.State
	urls  []string
}

func (m *menu) onReady() {
	copy := currentMenuCopy()
	systray.SetIcon(iconBytes("off"))
	systray.SetTooltip("frp-ngrok")

	m.status = systray.AddMenuItem(copy.Reading, "")
	m.status.Disable()
	m.server = systray.AddMenuItem("", "")
	m.server.Disable()

	systray.AddSeparator()
	m.open = systray.AddMenuItem(copy.Open, copy.OpenTip)

	systray.AddSeparator()
	m.tunnels = make([]*systray.MenuItem, tunnelSlots)
	for i := range m.tunnels {
		m.tunnels[i] = systray.AddMenuItem("", copy.ClickTunnel)
		m.tunnels[i].Hide()
	}
	m.more = systray.AddMenuItem("", "")
	m.more.Disable()
	m.more.Hide()

	systray.AddSeparator()
	m.toggle = systray.AddMenuItem(copy.Stop, "")
	m.restart = systray.AddMenuItem(copy.Reconnect, "")

	systray.AddSeparator()
	m.lang = systray.AddMenuItem(copy.Language, "")
	m.langEN = m.lang.AddSubMenuItemCheckbox(copy.English, "", store.Locale() == store.LocaleEN)
	m.langZH = m.lang.AddSubMenuItemCheckbox(copy.Chinese, "", store.Locale() == store.LocaleZH)
	m.plugins = systray.AddMenuItem(copy.Plugins, "")
	m.plugHK = m.plugins.AddSubMenuItemCheckbox(copy.Hotkeys, "", false)
	m.plugAL = m.plugins.AddSubMenuItemCheckbox(copy.AccessLog, "", false)
	m.plugPS = m.plugins.AddSubMenuItemCheckbox(copy.PortSites, "", false)

	systray.AddSeparator()
	m.quit = systray.AddMenuItem(copy.Quit, copy.QuitTip)

	m.wireClicks()
	m.refresh()

	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for range ticker.C {
			m.refresh()
		}
	}()
}

// wireClicks 为每个菜单项各起一个监听协程，避免巨大的 select。
func (m *menu) wireClicks() {
	go func() {
		for range m.open.ClickedCh {
			m.openURL(m.cli.ConsoleURL())
		}
	}()

	for i, item := range m.tunnels {
		go func(idx int, it *systray.MenuItem) {
			for range it.ClickedCh {
				m.mu.Lock()
				var url string
				if idx < len(m.urls) {
					url = m.urls[idx]
				}
				m.mu.Unlock()
				if url != "" {
					m.openURL(url)
				}
			}
		}(i, item)
	}

	go func() {
		for range m.toggle.ClickedCh {
			m.mu.Lock()
			stopped := m.state.Client.State == "stopped"
			m.mu.Unlock()
			if stopped {
				m.action("start")
			} else {
				m.action("stop")
			}
		}
	}()

	go func() {
		for range m.restart.ClickedCh {
			m.action("restart")
		}
	}()

	go func() {
		for range m.langEN.ClickedCh {
			m.setLocale(store.LocaleEN)
		}
	}()
	go func() {
		for range m.langZH.ClickedCh {
			m.setLocale(store.LocaleZH)
		}
	}()
	go func() {
		for range m.plugHK.ClickedCh {
			m.toggleHotkeys()
		}
	}()
	go func() {
		for range m.plugAL.ClickedCh {
			m.togglePlugin("/api/plugins/access-log", func(s apitypes.State) bool { return s.AccessLog })
		}
	}()
	go func() {
		for range m.plugPS.ClickedCh {
			m.togglePlugin("/api/plugins/port-sites", func(s apitypes.State) bool { return s.PortSites })
		}
	}()

	go func() {
		<-m.quit.ClickedCh
		systray.Quit()
	}()
}

// refresh 拉取最新状态并刷新菜单显示。
func (m *menu) refresh() {
	copy := currentMenuCopy()
	s, err := m.cli.State()
	if err != nil {
		if p := installer.ReadPort(); p > 0 && p != m.cli.Port() {
			m.cli.SetPort(p)
			s, err = m.cli.State()
		}
	}
	if err != nil {
		// 后台服务可能正在重启，如实降级显示，不拿旧状态糊弄。
		systray.SetIcon(iconBytes("bad"))
		systray.SetTooltip("frp-ngrok · " + copy.Unreachable)
		m.status.SetTitle(copy.Unreachable)
		m.server.SetTitle(err.Error())
		m.applyChrome(copy, store.Locale(), false, false, false, false)
		return
	}

	v := viewOf(s)
	systray.SetIcon(iconBytes(v.icon))
	systray.SetTooltip("frp-ngrok · " + v.tooltip)
	m.status.SetTitle(v.label)
	m.server.SetTitle(serverLine(s))
	m.applyChrome(copy, s.Locale, true, s.Hotkeys, s.AccessLog, s.PortSites)

	clientRunning := s.Client.State == "running"
	urls := make([]string, 0, len(s.Tunnels))
	for i, item := range m.tunnels {
		if i >= len(s.Tunnels) {
			item.Hide()
			continue
		}
		item.SetTitle(tunnelLabel(s.Tunnels[i]))
		item.SetIcon(tunnelDot(s.Tunnels[i], clientRunning))
		item.Show()
		urls = append(urls, s.Tunnels[i].URL)
	}
	if extra := len(s.Tunnels) - tunnelSlots; extra > 0 {
		m.more.SetTitle(store.T(fmt.Sprintf("…and %d more in the console", extra), fmt.Sprintf("…另有 %d 条，见控制台", extra)))
		m.more.Show()
	} else {
		m.more.Hide()
	}

	if s.Client.State == "stopped" {
		m.toggle.SetTitle(copy.Start)
	} else {
		m.toggle.SetTitle(copy.Stop)
	}

	m.mu.Lock()
	m.state = s
	m.urls = urls
	m.mu.Unlock()
}

func (m *menu) applyChrome(copy menuCopy, locale string, pluginsLive, hotkeys, accessLog, portSites bool) {
	m.open.SetTitle(copy.Open)
	m.restart.SetTitle(copy.Reconnect)
	m.quit.SetTitle(copy.Quit)
	m.lang.SetTitle(copy.Language)
	m.langEN.SetTitle(copy.English)
	m.langZH.SetTitle(copy.Chinese)
	m.plugins.SetTitle(copy.Plugins)
	m.plugHK.SetTitle(copy.Hotkeys)
	m.plugAL.SetTitle(copy.AccessLog)
	m.plugPS.SetTitle(copy.PortSites)
	setChecked(m.langEN, locale == store.LocaleEN)
	setChecked(m.langZH, locale == store.LocaleZH)
	setChecked(m.plugHK, hotkeys)
	setChecked(m.plugAL, accessLog)
	setChecked(m.plugPS, portSites)
	if pluginsLive {
		m.plugHK.Enable()
		m.plugAL.Enable()
		m.plugPS.Enable()
	} else {
		m.plugHK.Disable()
		m.plugAL.Disable()
		m.plugPS.Disable()
	}
}

func setChecked(item *systray.MenuItem, on bool) {
	if on {
		item.Check()
	} else {
		item.Uncheck()
	}
}

func (m *menu) setLocale(locale string) {
	if err := store.SavePrefs(store.Prefs{Locale: locale}); err != nil {
		log.Printf("保存语言失败: %v", err)
	}
	_ = m.cli.PutJSON("/api/prefs", store.Prefs{Locale: locale}, nil)
	m.refresh()
}

func (m *menu) toggleHotkeys() {
	var st apitypes.HotkeysState
	if err := m.cli.GetJSON("/api/plugins/hotkeys", &st); err != nil {
		log.Printf("读取快捷键插件失败: %v", err)
		m.refresh()
		return
	}
	body := store.HotkeyConfig{
		Enabled:      !st.Enabled,
		Items:        st.Items,
		OrderVersion: st.OrderVersion,
		PaletteCombo: st.PaletteCombo,
	}
	if err := m.cli.PutJSON("/api/plugins/hotkeys", body, nil); err != nil {
		log.Printf("切换快捷键插件失败: %v", err)
	}
	m.refresh()
}

func (m *menu) togglePlugin(path string, current func(apitypes.State) bool) {
	m.mu.Lock()
	on := current(m.state)
	m.mu.Unlock()
	if err := m.cli.PutJSON(path, map[string]bool{"enabled": !on}, nil); err != nil {
		log.Printf("切换插件失败 %s: %v", path, err)
	}
	m.refresh()
}

// action 异步执行客户端开关操作，完成后立即刷新菜单。
func (m *menu) action(name string) {
	go func() {
		if _, err := m.cli.ClientAction(name); err != nil {
			log.Printf("菜单栏操作 %s 失败: %v", name, err)
		}
		m.refresh()
	}()
}

func (m *menu) openURL(url string) {
	if err := installer.OpenBrowser(url); err != nil {
		log.Printf("打开链接失败: %v", err)
	}
}
