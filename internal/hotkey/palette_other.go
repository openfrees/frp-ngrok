//go:build !darwin || !cgo

package hotkey

import "github.com/openfrees/frp-ngrok/internal/store"

// ShowPalette 在非 macOS CGO 构建里不做事；这些平台本来也不会注册全局热键。
func ShowPalette(items []store.HotkeyItem, dispatch Dispatcher) {}
