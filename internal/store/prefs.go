package store

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

const (
	// LocaleEN 是新安装的默认界面语言。
	LocaleEN = "en"
	// LocaleZH 是简体中文。
	LocaleZH = "zh-CN"
)

// Prefs 是控制台级偏好，与隧道档案分开存放。
type Prefs struct {
	Locale string `json:"locale"`
}

// NormalizeLocale 只接受英文和简体中文，其它值一律无效。
func NormalizeLocale(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(raw, "_", "-"))) {
	case "en", "en-us", "en-gb":
		return LocaleEN, true
	case "zh", "zh-cn", "zh-hans":
		return LocaleZH, true
	default:
		return "", false
	}
}

// LoadPrefs 读取已保存的偏好。文件不存在或损坏时回到英文。
func LoadPrefs() Prefs {
	body, err := os.ReadFile(paths.PrefsFile())
	if err != nil {
		return Prefs{Locale: LocaleEN}
	}
	var prefs Prefs
	if json.Unmarshal(body, &prefs) != nil {
		return Prefs{Locale: LocaleEN}
	}
	locale, ok := NormalizeLocale(prefs.Locale)
	if !ok {
		return Prefs{Locale: LocaleEN}
	}
	prefs.Locale = locale
	return prefs
}

// SavePrefs 校验并落盘。未知语言直接拒绝，不改已有文件。
func SavePrefs(prefs Prefs) error {
	locale, ok := NormalizeLocale(prefs.Locale)
	if !ok {
		return fmt.Errorf("unsupported locale %q", prefs.Locale)
	}
	prefs.Locale = locale
	if err := os.MkdirAll(paths.AppDir(), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(paths.PrefsFile(), append(body, '\n'), 0o644)
}

// Locale 是当前界面语言，供托盘和接口共用。
func Locale() string {
	return LoadPrefs().Locale
}

// T 按已保存的语言返回英文或中文。新安装默认英文。
func T(en, zh string) string {
	if Locale() == LocaleZH {
		return zh
	}
	return en
}
