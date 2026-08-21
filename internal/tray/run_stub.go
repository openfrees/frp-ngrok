//go:build !darwin || !cgo

package tray

import "github.com/openfrees/frp-ngrok/internal/client"

// Supported 表示当前构建是否包含菜单栏能力。
//
// 菜单栏依赖系统原生界面接口，需要 macOS 且开启 CGO 才能编入。
func Supported() bool { return false }

// Run 在不支持的构建中直接返回。
func Run(_ *client.Client) {}
