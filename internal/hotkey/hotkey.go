// Package hotkey 负责「命令行工具快捷键」插件的运行时：注册系统级全局快捷键，
// 并在按键触发时执行对应的命令。
//
// 全局快捷键的注册是平台相关的：macOS 走 Carbon 的 RegisterEventHotKey，
// 事件由主线程的运行循环分发（见 mainloop_darwin.go）。命令执行与配置校验
// 不依赖平台，统一放在本包与 store 包。
package hotkey

import (
	"runtime"
	"sync"

	"github.com/openfrees/frp-ngrok/internal/store"
)

// LockMainThread 把调用它的 goroutine 钉到当前 OS 线程。
//
// macOS 上必须由 main goroutine 尽早调用（runDaemon 入口），随后的
// RunMainLoop 才会把 Carbon 事件循环跑在进程的初始主线程上——全局快捷键的
// 事件正是由这条线程的运行循环分发。晚一步 LockOSThread，goroutine 可能已经
// 被调度器迁走，事件循环就跑错线程了。
func LockMainThread() { runtime.LockOSThread() }

// Dispatcher 在快捷键触发时被调用，应快速返回（内部可再起 goroutine）。
type Dispatcher func(item store.HotkeyItem)

// PaletteOpener 在命令面板触发键被按下时打开快捷命令面板。
type PaletteOpener func(items []store.HotkeyItem, dispatch Dispatcher)

// engine 是平台相关的注册实现。register 成功返回 nil；失败时必须已自行清理，
// 不留下半截注册。
type engine interface {
	register(items []store.HotkeyItem, onFire func(id int)) error
	stop()
}

type engineFactory func() engine

// Manager 持有当前生效的快捷键集合，保证同一时刻只有一份注册。
type Manager struct {
	mu        sync.Mutex
	dispatch  Dispatcher
	palette   PaletteOpener
	newEngine engineFactory
	engine    engine
}

// NewManager 创建快捷键管理器。dispatch 为 nil 时按键触发只打日志不执行。
func NewManager(dispatch Dispatcher, palette ...PaletteOpener) *Manager {
	var open PaletteOpener
	if len(palette) > 0 {
		open = palette[0]
	}
	return newManagerWithEngine(dispatch, open, newEngine)
}

func newManagerWithEngine(dispatch Dispatcher, palette PaletteOpener, factory engineFactory) *Manager {
	if dispatch == nil {
		dispatch = func(store.HotkeyItem) {}
	}
	if palette == nil {
		palette = func([]store.HotkeyItem, Dispatcher) {}
	}
	return &Manager{dispatch: dispatch, palette: palette, newEngine: factory}
}

// Supported 返回当前平台是否支持全局快捷键。
func (m *Manager) Supported() bool { return platformSupported() }

// Apply 让 cfg 成为当前生效的配置：先停掉旧注册，再按需注册新的。
//
// 任一条注册失败即整体回退（什么都不注册），并返回错误，保证不会出现
// 「界面说开着、实际只有一半键能用」的半截状态。
func (m *Manager) Apply(cfg store.HotkeyConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := store.ValidateHotkeys(&cfg); err != nil {
		return err
	}
	m.teardownLocked()
	if !cfg.Enabled {
		return nil
	}

	items := append([]store.HotkeyItem(nil), cfg.Items...)
	registered := make([]store.HotkeyItem, 0, len(items)+1)
	callbacks := make([]func(), 0, len(items)+1)
	for _, it := range items {
		item := it
		if item.Combo == "" {
			continue
		}
		registered = append(registered, item)
		callbacks = append(callbacks, func() {
			go m.dispatch(item)
		})
	}
	registered = append(registered, store.HotkeyItem{
		ID:    "__frpanel_palette__",
		Name:  "命令面板",
		Combo: cfg.PaletteCombo,
	})
	callbacks = append(callbacks, func() {
		m.palette(items, m.dispatch)
	})

	eng := m.newEngine()
	if err := eng.register(registered, func(id int) {
		if id < 0 || id >= len(callbacks) {
			return
		}
		callbacks[id]()
	}); err != nil {
		eng.stop()
		return err
	}
	m.engine = eng
	return nil
}

// Stop 停掉全部注册，进程退出前调用。
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.teardownLocked()
}

func (m *Manager) teardownLocked() {
	if m.engine != nil {
		m.engine.stop()
		m.engine = nil
	}
}
