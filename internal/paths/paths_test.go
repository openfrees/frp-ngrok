package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppDirUsesNewNameOnFreshHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := AppDir()
	if !strings.HasSuffix(got, ".frp-ngrok") {
		t.Fatalf("fresh AppDir = %s, want ~/.frp-ngrok", got)
	}
}

func TestAppDirKeepsLegacyDirIfPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacy := filepath.Join(home, ".frpanel")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	got := AppDir()
	if got != legacy {
		t.Fatalf("legacy AppDir = %s, want %s", got, legacy)
	}
}

func TestInstalledBinUsesProductName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := InstalledBin()
	if !strings.Contains(got, string(os.PathSeparator)+"bin"+string(os.PathSeparator)+BinaryName) {
		t.Fatalf("InstalledBin = %s, want .../bin/%s", got, BinaryName)
	}
}

func TestInstalledBinUsesProductNameEvenIfLegacyBinaryExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyDir := filepath.Join(home, ".frpanel", "bin")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyBin := filepath.Join(legacyDir, LegacyBinaryName)
	if err := os.WriteFile(legacyBin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := InstalledBin()
	if !strings.HasSuffix(got, BinaryName) {
		t.Fatalf("upgrade must install %s, not keep %s; got %s", BinaryName, LegacyBinaryName, got)
	}
}
