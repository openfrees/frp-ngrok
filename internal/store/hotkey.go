// 命令行工具快捷键插件的配置读写与校验。
//
// 配置与档案/隧道数据分开存放（~/.frpanel/plugins/hotkeys.json），
// 插件启用与否、快捷键列表都只在这一个真源里。
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

// 快捷键执行动作的取值。
const (
	// HotkeyActionRun 直接在后台跑一条 shell 命令。
	HotkeyActionRun = "run"
	// HotkeyActionTerminal 打开系统自带「终端」并注入命令。
	HotkeyActionTerminal = "terminal"
	// HotkeyActionITerm 打开 iTerm2 并注入命令。
	HotkeyActionITerm = "iterm"
)

// DefaultPaletteCombo 是命令面板的默认触发键。
const DefaultPaletteCombo = "fn+space"

// HotkeyItem 是一条已配置的快捷键。
type HotkeyItem struct {
	// ID 在插件内唯一，前端生成；改条目时保持不变。
	ID   string `json:"id"`
	Name string `json:"name"`
	// Combo 是规范化后的组合键，形如 ctrl+opt+shift+cmd+r；空值表示只在命令面板里出现。
	Combo   string `json:"combo"`
	Action  string `json:"action"`
	Command string `json:"command"`
}

// HotkeyConfig 是快捷键插件的完整配置。
type HotkeyConfig struct {
	Enabled      bool         `json:"enabled"`
	Items        []HotkeyItem `json:"items"`
	OrderVersion int          `json:"orderVersion"`
	// PaletteCombo 是命令面板的触发键；空值按默认 fn+space 处理。
	PaletteCombo string `json:"paletteCombo"`
}

// modAliases 把各种叫法收敛到标准修饰键。
var modAliases = map[string]string{
	"cmd": "cmd", "command": "cmd", "meta": "cmd", "win": "cmd",
	"ctrl": "ctrl", "control": "ctrl",
	"opt": "opt", "option": "opt", "alt": "opt",
	"shift": "shift",
	"fn":    "fn",
}

// modOrder 是组合键规范化后的固定顺序，与 macOS 的 fn⌃⌥⇧⌘ 显示顺序一致。
var modOrder = []string{"fn", "ctrl", "opt", "shift", "cmd"}

// supportedKeys 是这一版支持的按键集合（不含修饰键）。
// 按键到系统键码的映射在 hotkey 包的平台实现里，这里只做合法性判断。
var supportedKeys = func() map[string]bool {
	m := make(map[string]bool)
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = true
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = true
	}
	for i := 1; i <= 12; i++ {
		m[fmt.Sprintf("f%d", i)] = true
	}
	for _, k := range []string{"space", "return", "escape", "tab", "backspace", "delete", "left", "right", "up", "down"} {
		m[k] = true
	}
	return m
}()

// NormalizeHotkeyCombo 把用户输入收敛成统一的 ctrl+opt+shift+cmd+key 形式。
//
// 只做语法收敛，不关心按键是否真有对应的系统键码（那是平台层的事），
// 因此「组合键没写错但当前系统不支持」这类情况能在这里通过、到注册时才报。
func NormalizeHotkeyCombo(raw string) (string, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(raw)), "+")
	mods := map[string]bool{}
	var key string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if mod, ok := modAliases[p]; ok {
			mods[mod] = true
			continue
		}
		// 非修饰键只能有一个，多出来的说明写岔了。
		if key != "" {
			return "", fmt.Errorf("组合键里只能有一个按键，%s 和 %s 撞一起了", key, p)
		}
		key = p
	}
	if key == "" {
		return "", fmt.Errorf("组合键缺少按键，例如 cmd+shift+r")
	}
	if len(mods) == 0 {
		return "", fmt.Errorf("组合键至少要有一个修饰键（cmd / ctrl / opt / shift / fn）")
	}
	if !supportedKeys[key] {
		return "", fmt.Errorf("暂不支持按键 %q：请用字母、数字或 F1-F12", key)
	}
	out := make([]string, 0, 5)
	for _, m := range modOrder {
		if mods[m] {
			out = append(out, m)
		}
	}
	out = append(out, key)
	return strings.Join(out, "+"), nil
}

// SplitHotkeyCombo 把规范化后的组合键拆成修饰键集合与按键。
// combo 应当来自 NormalizeHotkeyCombo；不合法时返回错误。
func SplitHotkeyCombo(combo string) (mods []string, key string, err error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(combo)), "+")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := modAliases[p]; ok {
			mods = append(mods, modAliases[p])
			continue
		}
		if key != "" {
			return nil, "", fmt.Errorf("组合键 %q 不合法", combo)
		}
		key = p
	}
	if key == "" || !supportedKeys[key] {
		return nil, "", fmt.Errorf("组合键 %q 不合法", combo)
	}
	if len(mods) == 0 {
		return nil, "", fmt.Errorf("组合键 %q 缺少修饰键", combo)
	}
	return mods, key, nil
}

// validHotkeyActions 是这一版支持的执行动作。
var validHotkeyActions = map[string]bool{
	HotkeyActionRun:      true,
	HotkeyActionTerminal: true,
	HotkeyActionITerm:    true,
}

// ValidateHotkeys 校验整份配置，并在原地把组合键规范化。
func ValidateHotkeys(c *HotkeyConfig) error {
	if c == nil {
		return fmt.Errorf("配置不能为空")
	}
	seenID := map[string]bool{}
	seenCombo := map[string]bool{}
	for i := range c.Items {
		it := &c.Items[i]
		it.ID = strings.TrimSpace(it.ID)
		it.Name = strings.TrimSpace(it.Name)
		it.Command = strings.TrimSpace(it.Command)

		if it.ID == "" {
			return fmt.Errorf("第 %d 条快捷键缺少标识", i+1)
		}
		if seenID[it.ID] {
			return fmt.Errorf("快捷键标识 %q 重复", it.ID)
		}
		seenID[it.ID] = true

		if it.Name == "" {
			return fmt.Errorf("第 %d 条快捷键缺少名称", i+1)
		}
		if !validHotkeyActions[it.Action] {
			return fmt.Errorf("第 %d 条（%s）的执行方式不合法", i+1, it.Name)
		}
		if it.Command == "" {
			return fmt.Errorf("第 %d 条（%s）的命令不能为空", i+1, it.Name)
		}

		rawCombo := strings.TrimSpace(it.Combo)
		if rawCombo == "" {
			it.Combo = ""
			continue
		}
		combo, err := NormalizeHotkeyCombo(rawCombo)
		if err != nil {
			return fmt.Errorf("第 %d 条（%s）的组合键不合法：%w", i+1, it.Name, err)
		}
		it.Combo = combo
		if seenCombo[it.Combo] {
			return fmt.Errorf("组合键 %s 已分配给另一条快捷键", it.Combo)
		}
		seenCombo[it.Combo] = true
	}

	// 命令面板触发键：空值用默认 fn+space，保证升级后的老配置自动获得该能力。
	if strings.TrimSpace(c.PaletteCombo) == "" {
		c.PaletteCombo = DefaultPaletteCombo
	}
	palette, err := NormalizeHotkeyCombo(c.PaletteCombo)
	if err != nil {
		return fmt.Errorf("命令面板触发键不合法：%w", err)
	}
	c.PaletteCombo = palette
	if seenCombo[palette] {
		return fmt.Errorf("命令面板触发键 %s 与某条快捷键撞了，换个组合", palette)
	}
	return nil
}

// LoadHotkeys 读取快捷键配置；文件不存在时返回空配置。
func LoadHotkeys() (HotkeyConfig, error) {
	var c HotkeyConfig
	data, err := os.ReadFile(paths.HotkeysFile())
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if len(data) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("解析快捷键配置失败: %w", err)
	}
	return c, nil
}

// SaveHotkeys 写入快捷键配置。命令可能含敏感信息，仅当前用户可读。
func SaveHotkeys(c HotkeyConfig) error {
	if err := os.MkdirAll(paths.PluginsDir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(paths.HotkeysFile(), append(data, '\n'), 0o600)
}
