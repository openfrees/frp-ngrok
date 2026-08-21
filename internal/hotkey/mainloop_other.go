//go:build !darwin || !cgo

package hotkey

// RunMainLoop 在没有原生事件循环的平台上阻塞到进程被信号终止。
func RunMainLoop() { select {} }
