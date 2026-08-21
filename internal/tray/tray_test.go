package tray

import (
	"testing"

	"github.com/openfrees/frp-ngrok/internal/paths"
	"github.com/openfrees/frp-ngrok/internal/store"
)

func TestCurrentMenuCopyFollowsSavedLocale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	en := currentMenuCopy()
	if en.Open != "Open console" {
		t.Fatalf("default locale should be English, open = %q", en.Open)
	}
	if en.Language != "Language / 语言" {
		t.Fatalf("language menu must stay bilingual so it is findable, got %q", en.Language)
	}

	if err := store.SavePrefs(store.Prefs{Locale: store.LocaleZH}); err != nil {
		t.Fatal(err)
	}
	zh := currentMenuCopy()
	if zh.Open != "打开控制台" || zh.Quit != "退出菜单栏图标" || zh.Stop != "停止隧道" {
		t.Fatalf("saved zh-CN should relabel the tray, got open=%q quit=%q stop=%q", zh.Open, zh.Quit, zh.Stop)
	}
	if zh.Language != "Language / 语言" {
		t.Fatalf("language parent must not flip with locale, got %q", zh.Language)
	}
}
