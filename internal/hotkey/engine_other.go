//go:build !darwin || !cgo

package hotkey

import (
	"fmt"

	"github.com/openfrees/frp-ngrok/internal/store"
)

// noopEngine 在无法注册全局快捷键的平台上占位，register 直接报错。
type noopEngine struct{}

func newEngine() engine       { return noopEngine{} }
func platformSupported() bool { return false }

func (noopEngine) register(items []store.HotkeyItem, onFire func(int)) error {
	return fmt.Errorf("当前系统不支持全局快捷键")
}

func (noopEngine) stop() {}
