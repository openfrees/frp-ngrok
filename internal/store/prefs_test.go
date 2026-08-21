package store

import (
	"os"
	"strings"
	"testing"

	"github.com/openfrees/frp-ngrok/internal/paths"
)

func TestLoadPrefsDefaultsToEnglishWhenFileMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := LoadPrefs()
	if got.Locale != LocaleEN {
		t.Fatalf("fresh install locale = %q, want %q", got.Locale, LocaleEN)
	}
}

func TestSavePrefsPersistsLocaleAcrossReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SavePrefs(Prefs{Locale: LocaleZH}); err != nil {
		t.Fatal(err)
	}
	got := LoadPrefs()
	if got.Locale != LocaleZH {
		t.Fatalf("reloaded locale = %q, want %q", got.Locale, LocaleZH)
	}
	body, err := os.ReadFile(paths.PrefsFile())
	if err != nil {
		t.Fatalf("prefs file not written: %v", err)
	}
	if !strings.Contains(string(body), `"locale"`) || !strings.Contains(string(body), "zh-CN") {
		t.Fatalf("prefs.json should record zh-CN, got %s", body)
	}
}

func TestSavePrefsRejectsUnknownLocale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SavePrefs(Prefs{Locale: "fr"}); err == nil {
		t.Fatal("unknown locale should be rejected")
	}
	if got := LoadPrefs().Locale; got != LocaleEN {
		t.Fatalf("rejected save must leave default English, got %q", got)
	}
}

func TestSavePrefsAcceptsZhAlias(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SavePrefs(Prefs{Locale: "zh"}); err != nil {
		t.Fatal(err)
	}
	if got := LoadPrefs().Locale; got != LocaleZH {
		t.Fatalf("zh should normalize to zh-CN, got %q", got)
	}
}
