//go:build darwin && cgo

package hotkey

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include <stdlib.h>

extern void frpPaletteCommandSelected(char *id);
static void frpHidePalette(void);

@interface FrpPaletteWindow : NSWindow
@end

@implementation FrpPaletteWindow
- (BOOL)canBecomeKeyWindow { return YES; }
- (BOOL)canBecomeMainWindow { return YES; }
- (void)resignKeyWindow {
	[super resignKeyWindow];
	[self orderOut:nil];
}
@end

@interface FrpPaletteHandler : NSObject <WKScriptMessageHandler>
@end

@implementation FrpPaletteHandler
- (void)userContentController:(WKUserContentController *)userContentController
      didReceiveScriptMessage:(WKScriptMessage *)message {
	if (![message.body isKindOfClass:[NSString class]]) {
		return;
	}
	NSString *sid = (NSString *)message.body;
	if ([sid isEqualToString:@"__close__"]) {
		frpHidePalette();
		return;
	}
	frpHidePalette();
	frpPaletteCommandSelected((char *)[sid UTF8String]);
}
@end

static FrpPaletteWindow *frpPaletteWindow = nil;
static WKWebView *frpPaletteWebView = nil;
static FrpPaletteHandler *frpPaletteHandler = nil;

static void frpEnsurePalette(void) {
	if (frpPaletteWindow != nil) {
		return;
	}
	NSRect frame = NSMakeRect(0, 0, 760, 510);
	frpPaletteWindow = [[FrpPaletteWindow alloc]
		initWithContentRect:frame
		styleMask:NSWindowStyleMaskBorderless
		backing:NSBackingStoreBuffered
		defer:NO];
	[frpPaletteWindow setOpaque:NO];
	[frpPaletteWindow setBackgroundColor:[NSColor clearColor]];
	[frpPaletteWindow setHasShadow:NO];
	[frpPaletteWindow setLevel:NSFloatingWindowLevel];
	[frpPaletteWindow setReleasedWhenClosed:NO];
	[frpPaletteWindow setCollectionBehavior:
		NSWindowCollectionBehaviorCanJoinAllSpaces |
		NSWindowCollectionBehaviorFullScreenAuxiliary];

	WKWebViewConfiguration *config = [[WKWebViewConfiguration alloc] init];
	WKUserContentController *controller = [[WKUserContentController alloc] init];
	frpPaletteHandler = [[FrpPaletteHandler alloc] init];
	[controller addScriptMessageHandler:frpPaletteHandler name:@"frpanel"];
	[config setUserContentController:controller];

	frpPaletteWebView = [[WKWebView alloc] initWithFrame:frame configuration:config];
	[frpPaletteWebView setValue:@NO forKey:@"drawsBackground"];
	[frpPaletteWindow setContentView:frpPaletteWebView];
}

static void frpHidePalette(void) {
	if (frpPaletteWindow != nil) {
		[frpPaletteWindow orderOut:nil];
	}
}

static void frpTogglePalette(const char *html) {
	@autoreleasepool {
		frpEnsurePalette();
		if ([frpPaletteWindow isVisible]) {
			frpHidePalette();
			return;
		}
		NSString *page = [NSString stringWithUTF8String:(html == NULL ? "" : html)];
		[frpPaletteWebView loadHTMLString:page baseURL:nil];
		[frpPaletteWindow center];
		[NSApp activateIgnoringOtherApps:YES];
		[frpPaletteWindow makeKeyAndOrderFront:nil];
		[frpPaletteWindow makeFirstResponder:frpPaletteWebView];
	}
}
*/
import "C"

import (
	"encoding/json"
	"sync"
	"unsafe"

	"github.com/openfrees/frp-ngrok/internal/store"
)

var paletteState = struct {
	sync.Mutex
	items    map[string]store.HotkeyItem
	dispatch Dispatcher
}{}

type paletteCommand struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Combo   string `json:"combo"`
	Action  string `json:"action"`
	Command string `json:"command"`
}

// ShowPalette 在屏幕中央显示 Spotlight 式命令面板。
func ShowPalette(items []store.HotkeyItem, dispatch Dispatcher) {
	paletteState.Lock()
	paletteState.items = make(map[string]store.HotkeyItem, len(items))
	for _, it := range items {
		paletteState.items[it.ID] = it
	}
	paletteState.dispatch = dispatch
	paletteState.Unlock()

	cHTML := C.CString(paletteHTML(items))
	defer C.free(unsafe.Pointer(cHTML))
	C.frpTogglePalette(cHTML)
}

func paletteHTML(items []store.HotkeyItem) string {
	commands := make([]paletteCommand, 0, len(items))
	for _, it := range items {
		commands = append(commands, paletteCommand{
			ID:      it.ID,
			Name:    it.Name,
			Combo:   it.Combo,
			Action:  it.Action,
			Command: it.Command,
		})
	}
	data, err := json.Marshal(commands)
	if err != nil {
		data = []byte("[]")
	}
	jsonText := string(data)
	return `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<style>
:root {
  color-scheme: light;
  --glass: rgba(248, 250, 252, 0.88);
  --glass-strong: rgba(255, 255, 255, 0.96);
  --line: rgba(71, 85, 105, 0.15);
  --text: #111827;
  --dim: #64748b;
  --faint: #94a3b8;
  --signal: #2563eb;
  --signal-2: #06b6d4;
  --signal-soft: rgba(37, 99, 235, 0.08);
  --signal-line: rgba(37, 99, 235, 0.18);
}
* { box-sizing: border-box; }
html, body { width: 100%; height: 100%; margin: 0; overflow: hidden; background: transparent; }
body {
  font-family: "PingFang SC", "Hiragino Sans GB", system-ui, -apple-system, sans-serif;
  color: var(--text);
  -webkit-font-smoothing: antialiased;
}
.shell {
  width: 100%; height: 100%;
  padding: 48px;
  background: transparent;
}
.panel {
  position: relative;
  height: 100%;
  border: 1px solid rgba(71, 85, 105, 0.16);
  border-radius: 24px;
  background:
    radial-gradient(circle at 12% 0%, rgba(96, 165, 250, 0.12), transparent 34%),
    radial-gradient(circle at 88% 8%, rgba(148, 163, 184, 0.12), transparent 30%),
    linear-gradient(180deg, var(--glass-strong), var(--glass));
  box-shadow:
    0 16px 28px rgba(8, 20, 15, 0.18),
    0 4px 12px rgba(8, 20, 15, 0.10),
    inset 0 1px 0 rgba(255,255,255,0.86);
  -webkit-backdrop-filter: blur(26px) saturate(1.18);
  backdrop-filter: blur(26px) saturate(1.18);
  overflow: hidden;
}
.panel::before {
  content: "";
  position: absolute;
  inset: 0;
  pointer-events: none;
  border-radius: inherit;
  background:
    linear-gradient(90deg, rgba(96, 165, 250, 0.18), transparent 28%, transparent 72%, rgba(148, 163, 184, 0.14)),
    linear-gradient(180deg, rgba(255,255,255,0.46), transparent 18%);
  opacity: 0.68;
}
.search {
  position: relative;
  z-index: 1;
  height: 82px;
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 18px 24px 14px;
  border-bottom: 1px solid rgba(71, 85, 105, 0.11);
  background: linear-gradient(180deg, rgba(255,255,255,0.42), rgba(255,255,255,0.08));
}
.mark {
  width: 34px; height: 34px;
  border-radius: 12px;
  background: linear-gradient(145deg, #111827, #1f2937);
  color: #e0f2fe;
  display: grid;
  place-items: center;
  font-size: 18px;
  font-weight: 700;
  flex: 0 0 auto;
  box-shadow: 0 10px 24px rgba(15, 23, 42, 0.20), inset 0 1px 0 rgba(255,255,255,0.12);
}
input {
  width: 100%;
  border: 0;
  outline: 0;
  background: transparent;
  font-size: 28px;
  line-height: 1;
  font-weight: 620;
  letter-spacing: 0;
  color: var(--text);
}
input::placeholder { color: #9ca3af; }
.list {
  position: relative;
  z-index: 1;
  height: calc(100% - 82px);
  overflow-y: auto;
  padding: 10px;
}
.item {
  position: relative;
  min-height: 66px;
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 12px;
  align-items: center;
  padding: 10px 14px;
  border-radius: 14px;
  border: 1px solid transparent;
}
.item.active {
  background:
    linear-gradient(90deg, rgba(37, 99, 235, 0.10), rgba(6, 182, 212, 0.06)),
    rgba(255,255,255,0.38);
  border-color: var(--signal-line);
  box-shadow: inset 0 1px 0 rgba(255,255,255,0.62), 0 8px 22px rgba(37, 99, 235, 0.07);
}
.item.active::before {
  content: "";
  position: absolute;
  left: 0;
  top: 12px;
  bottom: 12px;
  width: 3px;
  border-radius: 999px;
  background: linear-gradient(180deg, var(--signal-2), var(--signal));
}
.title {
  font-size: 15px;
  font-weight: 650;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.meta {
  margin-top: 2px;
  color: var(--dim);
  font-size: 12.5px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.combo {
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
  color: var(--signal);
  background: rgba(37, 99, 235, 0.08);
  border: 1px solid var(--signal-line);
  border-radius: 999px;
  padding: 4px 9px;
  font-size: 12px;
  white-space: nowrap;
  box-shadow: inset 0 1px 0 rgba(255,255,255,0.58);
}
.empty {
  height: 100%;
  display: grid;
  place-items: center;
  color: var(--faint);
  font-size: 14px;
}
</style>
</head>
<body>
<div class="shell">
  <div class="panel">
    <div class="search">
      <div class="mark">⌘</div>
      <input id="q" placeholder="搜索快捷命令" autocomplete="off" spellcheck="false">
    </div>
    <div id="list" class="list"></div>
  </div>
</div>
<script>
const commands = ` + jsonText + `;
const actionLabel = { run: "直接运行", terminal: "打开终端", iterm: "打开 iTerm" };
const symbol = { fn: "fn", ctrl: "⌃", opt: "⌥", shift: "⇧", cmd: "⌘" };
const keyLabel = { space: "Space", return: "↩", escape: "Esc", tab: "Tab", backspace: "⌫", delete: "⌦", left: "←", right: "→", up: "↑", down: "↓" };
const q = document.getElementById("q");
const list = document.getElementById("list");
let active = 0;
function comboText(combo) {
  if (!combo) return "面板";
  return String(combo || "").split("+").map(p => symbol[p] || keyLabel[p] || p.toUpperCase()).join("");
}
function filtered() {
  const term = q.value.trim().toLowerCase();
  if (!term) return commands;
  return commands.filter(c => [c.name, c.command, actionLabel[c.action] || ""].join(" ").toLowerCase().includes(term));
}
function run(id) {
  window.webkit.messageHandlers.frpanel.postMessage(id);
}
function render() {
  const rows = filtered();
  if (active >= rows.length) active = Math.max(0, rows.length - 1);
  list.innerHTML = "";
  if (!rows.length) {
    const empty = document.createElement("div");
    empty.className = "empty";
    empty.textContent = commands.length ? "没有匹配的命令" : "还没有配置快捷命令";
    list.appendChild(empty);
    return;
  }
  rows.forEach((cmd, idx) => {
    const item = document.createElement("div");
    item.className = "item" + (idx === active ? " active" : "");
    item.onmousemove = () => { active = idx; render(); };
    item.onclick = () => run(cmd.id);
    const main = document.createElement("div");
    const title = document.createElement("div");
    title.className = "title";
    title.textContent = cmd.name;
    const meta = document.createElement("div");
    meta.className = "meta";
    meta.textContent = (actionLabel[cmd.action] || cmd.action) + " · " + cmd.command;
    main.appendChild(title);
    main.appendChild(meta);
    const combo = document.createElement("div");
    combo.className = "combo";
    combo.textContent = comboText(cmd.combo);
    item.appendChild(main);
    item.appendChild(combo);
    list.appendChild(item);
  });
  const current = list.children[active];
  if (current) current.scrollIntoView({ block: "nearest" });
}
q.addEventListener("input", () => { active = 0; render(); });
document.addEventListener("keydown", e => {
  const rows = filtered();
  if (e.key === "Escape") {
    e.preventDefault();
    window.webkit.messageHandlers.frpanel.postMessage("__close__");
  } else if (e.key === "ArrowDown") {
    e.preventDefault();
    if (rows.length) active = (active + 1) % rows.length;
    render();
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    if (rows.length) active = (active - 1 + rows.length) % rows.length;
    render();
  } else if (e.key === "Enter") {
    e.preventDefault();
    if (rows[active]) run(rows[active].id);
  }
}, true);
render();
setTimeout(() => q.focus(), 80);
</script>
</body>
</html>`
}
