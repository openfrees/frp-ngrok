//go:build darwin && cgo

package hotkey

import (
	"strings"
	"testing"
)

func TestPaletteHTMLUsesSoftRoundedGlassStyle(t *testing.T) {
	html := paletteHTML(nil)
	for _, want := range []string{
		"padding: 48px;",
		"border-radius: 24px;",
		"0 16px 28px rgba(8, 20, 15, 0.18)",
		"inset 0 1px 0 rgba(255,255,255,0.86)",
		"--signal: #2563eb;",
		"background: linear-gradient(145deg, #111827, #1f2937);",
		".panel::before",
		".item.active::before",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("palette HTML should contain %q", want)
		}
	}
	if strings.Contains(html, "80px rgba") || strings.Contains(html, "90px rgba") {
		t.Fatal("palette shadow blur is too large and will be clipped by the transparent WebView frame")
	}
	for _, oldGreen := range []string{"#07875d", "#123a2c", "7, 135, 93"} {
		if strings.Contains(html, oldGreen) {
			t.Fatalf("palette should avoid the old mint green accent %q", oldGreen)
		}
	}
}
