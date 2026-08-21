//go:build darwin && cgo

package hotkey

/*
#cgo LDFLAGS: -framework Carbon
#include <Carbon/Carbon.h>
#include <stdint.h>

// 事件处理器运行在主线程的运行循环里，通过 //export 回调回 Go。
extern void frpHotkeyFired(uint32_t id);

static OSStatus frpHotkeyHandler(EventHandlerCallRef inCallRef, EventRef inEvent, void *userData) {
	EventHotKeyID hotKeyID;
	if (GetEventParameter(inEvent, kEventParamDirectObject, typeEventHotKeyID,
		NULL, sizeof(hotKeyID), NULL, &hotKeyID) != noErr) {
		return noErr;
	}
	frpHotkeyFired((uint32_t)hotKeyID.id);
	return noErr;
}

// frpInstallHandler 装一次事件处理器；重复调用无害。
static void frpInstallHandler(void) {
	static EventHandlerRef handlerRef = NULL;
	if (handlerRef != NULL) {
		return;
	}
	EventTypeSpec spec;
	spec.eventClass = kEventClassKeyboard;
	spec.eventKind = kEventHotKeyPressed;
	InstallEventHandler(GetApplicationEventTarget(), frpHotkeyHandler, 1, &spec, NULL, &handlerRef);
}

// frpRegisterHotKey 注册一条快捷键，成功返回句柄、失败返回 NULL。
static void *frpRegisterHotKey(uint32_t keyCode, uint32_t modifiers, uint32_t id) {
	EventHotKeyID hotKeyID = {'frp1', id};
	EventHotKeyRef ref = NULL;
	OSStatus err = RegisterEventHotKey((unsigned int)keyCode, (unsigned int)modifiers,
		hotKeyID, GetApplicationEventTarget(), 0, &ref);
	if (err != noErr) {
		return NULL;
	}
	return (void *)ref;
}

static void frpUnregisterHotKey(void *ref) {
	if (ref != NULL) {
		UnregisterEventHotKey((EventHotKeyRef)ref);
	}
}
*/
import "C"

import (
	"fmt"
	"log"
	"sync"
	"unsafe"

	"github.com/openfrees/frp-ngrok/internal/store"
)

// Carbon 修饰键掩码（EventModifiers）。
const (
	maskCmd   = 0x0100 // cmdKey
	maskShift = 0x0200 // shiftKey
	maskOpt   = 0x0800 // optionKey
	maskCtrl  = 0x1000 // controlKey
	maskFn    = 0x20000
)

// 当前生效的回调，由运行在主线程的 C 处理器读取后派发回 Go。
var (
	fireMu sync.Mutex
	fireFn func(int)
)

//export frpHotkeyFired
func frpHotkeyFired(id uint32) {
	// 打在 handler 入口：能区分「事件没到 handler」还是「到了但后续执行失败」。
	log.Printf("[快捷键] 收到热键事件 id=%d", id)
	fireMu.Lock()
	fn := fireFn
	fireMu.Unlock()
	if fn != nil {
		fn(int(id))
	}
}

// darwinEngine 是 macOS 上的注册实现。
type darwinEngine struct {
	refs []unsafe.Pointer
}

func newEngine() engine       { return &darwinEngine{} }
func platformSupported() bool { return true }
func (e *darwinEngine) stop() { e.clear() }

// installHandler 安装 Carbon 事件处理器（幂等）。必须在主线程调用，
// 由 RunMainLoop 在进入事件循环前执行一次。
func installHandler() { C.frpInstallHandler() }

func (e *darwinEngine) register(items []store.HotkeyItem, onFire func(int)) error {
	fireMu.Lock()
	fireFn = onFire
	fireMu.Unlock()

	for i, it := range items {
		mods, key, err := store.SplitHotkeyCombo(it.Combo)
		if err != nil {
			e.clear()
			return err
		}
		keyCode, err := keyToCode(key)
		if err != nil {
			e.clear()
			return err
		}
		ref := C.frpRegisterHotKey(C.uint32_t(keyCode), C.uint32_t(modsToMask(mods)), C.uint32_t(i))
		if ref == nil {
			e.clear()
			return fmt.Errorf("快捷键 %s 注册失败，可能已被其他程序占用", it.Combo)
		}
		e.refs = append(e.refs, ref)
	}
	return nil
}

// clear 撤掉回调与全部已注册的快捷键。
func (e *darwinEngine) clear() {
	fireMu.Lock()
	fireFn = nil
	fireMu.Unlock()
	for _, ref := range e.refs {
		C.frpUnregisterHotKey(ref)
	}
	e.refs = nil
}

// modsToMask 把修饰键集合转成 Carbon 掩码。
func modsToMask(mods []string) uint32 {
	var mask uint32
	for _, m := range mods {
		switch m {
		case "cmd":
			mask |= maskCmd
		case "shift":
			mask |= maskShift
		case "opt":
			mask |= maskOpt
		case "ctrl":
			mask |= maskCtrl
		case "fn":
			mask |= maskFn
		}
	}
	return mask
}

// keyToCode 把按键名转成美式布局的虚拟键码。
func keyToCode(key string) (uint32, error) {
	if len(key) == 1 {
		c := key[0]
		if c >= 'a' && c <= 'z' {
			return letterKeyCodes[c], nil
		}
		if c >= '0' && c <= '9' {
			return digitKeyCodes[c], nil
		}
	}
	if code, ok := specialKeyCodes[key]; ok {
		return code, nil
	}
	return 0, fmt.Errorf("暂不支持按键 %q", key)
}

// 美式 ANSI 布局的虚拟键码（kVK_ANSI_* / kVK_F* / 常用功能键）。
var letterKeyCodes = map[byte]uint32{
	'a': 0x00, 'b': 0x0B, 'c': 0x08, 'd': 0x02, 'e': 0x0E, 'f': 0x03,
	'g': 0x05, 'h': 0x04, 'i': 0x22, 'j': 0x26, 'k': 0x28, 'l': 0x25,
	'm': 0x2E, 'n': 0x2D, 'o': 0x1F, 'p': 0x23, 'q': 0x0C, 'r': 0x0F,
	's': 0x01, 't': 0x11, 'u': 0x20, 'v': 0x09, 'w': 0x0D, 'x': 0x07,
	'y': 0x10, 'z': 0x06,
}

var digitKeyCodes = map[byte]uint32{
	'0': 0x1D, '1': 0x12, '2': 0x13, '3': 0x14, '4': 0x15,
	'5': 0x17, '6': 0x16, '7': 0x1A, '8': 0x1C, '9': 0x19,
}

var specialKeyCodes = map[string]uint32{
	"f1": 0x7A, "f2": 0x78, "f3": 0x63, "f4": 0x76, "f5": 0x60, "f6": 0x61,
	"f7": 0x62, "f8": 0x64, "f9": 0x65, "f10": 0x6D, "f11": 0x67, "f12": 0x6F,
	"space": 0x31, "return": 0x24, "escape": 0x35, "tab": 0x30,
	"backspace": 0x33, "delete": 0x75,
	"left": 0x7B, "right": 0x7C, "up": 0x7E, "down": 0x7D,
}
