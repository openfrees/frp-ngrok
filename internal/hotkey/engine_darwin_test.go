//go:build darwin && cgo

package hotkey

import "testing"

func TestModsToMaskIncludesFn(t *testing.T) {
	if got := modsToMask([]string{"fn"}); got != maskFn {
		t.Fatalf("fn 掩码 = %#x, 想要 %#x", got, maskFn)
	}
}
