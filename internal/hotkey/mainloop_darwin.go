//go:build darwin && cgo

package hotkey

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#include <Carbon/Carbon.h>
#import <Cocoa/Cocoa.h>

static void frpPrepareApplication(void) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
}

// frpRunLoop 交给 Cocoa 的 NSApplication 驱动主线程事件循环。
// NSApp run 内部会拉取并分发 Carbon 热键事件，同时也能驱动 WKWebView 命令面板。
static void frpRunLoop(void) {
	[NSApp run];
}
*/
import "C"

import "runtime"

// RunMainLoop 阻塞调用线程，交给 NSApplication 分发系统事件。
//
// RegisterEventHotKey 的事件和 WKWebView 的 UI 事件都必须由初始主线程处理。
// 调用方需先通过 LockMainThread 把 main goroutine 钉在初始主线程。
func RunMainLoop() {
	runtime.LockOSThread()
	C.frpPrepareApplication()
	installHandler()
	C.frpRunLoop()
}
