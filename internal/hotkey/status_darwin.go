//go:build darwin && cgo

package hotkey

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <Cocoa/Cocoa.h>
#include <dispatch/dispatch.h>
#include <stdlib.h>
#include <string.h>

extern void frpRunStatusInput(int id, char *text);
extern void frpRunStatusUserClosed(int id);

typedef struct {
	int id;
	int slot;
	char *name;
	char *command;
} FrpRunStatusOpenMessage;

typedef struct {
	int id;
	int color;
	int bold;
	char *text;
} FrpRunStatusTextMessage;

typedef struct {
	int id;
} FrpRunStatusCloseMessage;

static NSMutableDictionary *frpRunStatusWindows = nil;
static NSMutableDictionary *frpRunStatusViews = nil;
static NSMutableDictionary *frpRunStatusCountdownRanges = nil;
static NSMutableDictionary *frpRunStatusOutputStarts = nil;
static NSMutableDictionary *frpRunStatusDelegates = nil;

@interface FrpRunStatusDelegate : NSObject <NSWindowDelegate>
@property(nonatomic) int frpStatusID;
@end

@implementation FrpRunStatusDelegate
- (void)windowWillClose:(NSNotification *)notification {
	int sid = self.frpStatusID;
	NSNumber *key = [NSNumber numberWithInt:sid];
	[frpRunStatusWindows removeObjectForKey:key];
	[frpRunStatusViews removeObjectForKey:key];
	[frpRunStatusCountdownRanges removeObjectForKey:key];
	[frpRunStatusOutputStarts removeObjectForKey:key];
	[frpRunStatusDelegates removeObjectForKey:key];
	frpRunStatusUserClosed(sid);
}
@end

@interface FrpRunStatusTextView : NSTextView
@property(nonatomic) int frpStatusID;
@end

@implementation FrpRunStatusTextView
- (void)frpSendInput:(NSString *)text {
	if (text == nil || [text length] == 0) {
		return;
	}
	frpRunStatusInput(self.frpStatusID, (char *)[text UTF8String]);
}

- (void)keyDown:(NSEvent *)event {
	unsigned short code = [event keyCode];
	if (code == 36 || code == 76) {
		[self frpSendInput:@"\r"];
		return;
	}
	if (code == 51) {
		[self frpSendInput:@"\177"];
		return;
	}
	if (code == 126) {
		[self frpSendInput:@"\033[A"];
		return;
	}
	if (code == 125) {
		[self frpSendInput:@"\033[B"];
		return;
	}
	if (code == 123) {
		[self frpSendInput:@"\033[D"];
		return;
	}
	if (code == 124) {
		[self frpSendInput:@"\033[C"];
		return;
	}
	[self frpSendInput:[event characters]];
}

- (void)paste:(id)sender {
	NSString *text = [[NSPasteboard generalPasteboard] stringForType:NSPasteboardTypeString];
	[self frpSendInput:text];
}
@end

static NSString *frpRunStatusString(char *s) {
	if (s == NULL) {
		return @"";
	}
	return [NSString stringWithUTF8String:s] ?: @"";
}

static void frpEnsureRunStatusStore(void) {
	if (frpRunStatusWindows == nil) {
		frpRunStatusWindows = [[NSMutableDictionary alloc] init];
		frpRunStatusViews = [[NSMutableDictionary alloc] init];
		frpRunStatusCountdownRanges = [[NSMutableDictionary alloc] init];
		frpRunStatusOutputStarts = [[NSMutableDictionary alloc] init];
		frpRunStatusDelegates = [[NSMutableDictionary alloc] init];
	}
}

static NSColor *frpRunStatusTextColor(int color) {
	switch (color) {
		case 1:
			return [NSColor colorWithCalibratedRed:1.00 green:0.42 blue:0.42 alpha:1.0];
		case 2:
			return [NSColor colorWithCalibratedRed:0.29 green:0.88 blue:0.50 alpha:1.0];
		case 3:
			return [NSColor colorWithCalibratedRed:0.98 green:0.80 blue:0.22 alpha:1.0];
		case 4:
			return [NSColor colorWithCalibratedRed:0.38 green:0.65 blue:1.00 alpha:1.0];
		case 5:
			return [NSColor colorWithCalibratedRed:0.79 green:0.52 blue:1.00 alpha:1.0];
		case 6:
			return [NSColor colorWithCalibratedRed:0.22 green:0.84 blue:0.92 alpha:1.0];
		case 7:
			return [NSColor colorWithCalibratedRed:0.96 green:0.98 blue:1.00 alpha:1.0];
		default:
			return [NSColor colorWithWhite:0.92 alpha:1.0];
	}
}

static NSDictionary *frpRunStatusTextAttributes(int color, int bold) {
	return @{
		NSForegroundColorAttributeName: frpRunStatusTextColor(color),
		NSFontAttributeName: [NSFont monospacedSystemFontOfSize:12 weight:(bold ? NSFontWeightSemibold : NSFontWeightRegular)]
	};
}

static void frpAppendToTextView(NSTextView *textView, NSString *text, int color, int bold) {
	if (textView == nil || text == nil || [text length] == 0) {
		return;
	}
	NSAttributedString *chunk = [[NSAttributedString alloc] initWithString:text attributes:frpRunStatusTextAttributes(color, bold)];
	[[textView textStorage] appendAttributedString:chunk];
}

static void frpApplySGRParams(NSString *params, int *color, int *bold) {
	if (params == nil || [params length] == 0) {
		*color = 0;
		*bold = 0;
		return;
	}
	for (NSString *raw in [params componentsSeparatedByString:@";"]) {
		int code = [raw intValue];
		switch (code) {
			case 0:
				*color = 0;
				*bold = 0;
				break;
			case 1:
				*bold = 1;
				break;
			case 22:
				*bold = 0;
				break;
			case 31:
			case 91:
				*color = 1;
				break;
			case 32:
			case 92:
				*color = 2;
				break;
			case 33:
			case 93:
				*color = 3;
				break;
			case 34:
			case 94:
				*color = 4;
				break;
			case 35:
			case 95:
				*color = 5;
				break;
			case 36:
			case 96:
				*color = 6;
				break;
			case 37:
			case 97:
				*color = 7;
				break;
			case 30:
			case 39:
			case 90:
				*color = 0;
				break;
			default:
				break;
		}
	}
}

static void frpAppendANSIText(NSTextView *textView, NSString *text) {
	if (textView == nil || text == nil || [text length] == 0) {
		return;
	}
	const char *p = [text UTF8String];
	if (p == NULL) {
		return;
	}
	NSMutableData *buf = [NSMutableData data];
	int color = 0;
	int bold = 0;
	for (const char *s = p; *s != '\0'; ) {
		if (*s == 0x1b && *(s + 1) == '[') {
			const char *j = s + 2;
			while (*j != '\0' && (*j < '@' || *j > '~')) {
				j++;
			}
			if (*j == '\0') {
				[buf appendBytes:s length:strlen(s)];
				break;
			}
			if (*j == 'm') {
				if ([buf length] > 0) {
					NSString *chunk = [[NSString alloc] initWithData:buf encoding:NSUTF8StringEncoding];
					frpAppendToTextView(textView, chunk, color, bold);
					[buf setLength:0];
				}
				NSUInteger paramLen = (NSUInteger)(j - (s + 2));
				NSString *params = paramLen == 0 ? @"" :
					[[NSString alloc] initWithBytes:s + 2 length:paramLen encoding:NSUTF8StringEncoding];
				frpApplySGRParams(params, &color, &bold);
			}
			s = j + 1;
			continue;
		}
		[buf appendBytes:s length:1];
		s++;
	}
	if ([buf length] > 0) {
		NSString *chunk = [[NSString alloc] initWithData:buf encoding:NSUTF8StringEncoding];
		frpAppendToTextView(textView, chunk, color, bold);
	}
}

static void frpReplaceCountdownLine(int id, NSTextView *textView, NSString *text) {
	if (textView == nil || text == nil) {
		return;
	}
	NSNumber *key = [NSNumber numberWithInt:id];
	NSValue *oldRangeValue = [frpRunStatusCountdownRanges objectForKey:key];
	NSAttributedString *chunk = [[NSAttributedString alloc] initWithString:text attributes:frpRunStatusTextAttributes(0, 0)];
	if (oldRangeValue != nil) {
		NSRange oldRange = [oldRangeValue rangeValue];
		if (NSMaxRange(oldRange) <= [[textView textStorage] length]) {
			[[textView textStorage] replaceCharactersInRange:oldRange withAttributedString:chunk];
			[frpRunStatusCountdownRanges setObject:[NSValue valueWithRange:NSMakeRange(oldRange.location, [text length])] forKey:key];
			[textView scrollRangeToVisible:NSMakeRange([[textView string] length], 0)];
			return;
		}
	}
	NSUInteger loc = [[textView textStorage] length];
	[[textView textStorage] appendAttributedString:[[NSAttributedString alloc] initWithString:@"\n" attributes:frpRunStatusTextAttributes(0, 0)]];
	loc = [[textView textStorage] length];
	[[textView textStorage] appendAttributedString:chunk];
	[frpRunStatusCountdownRanges setObject:[NSValue valueWithRange:NSMakeRange(loc, [text length])] forKey:key];
	[textView scrollRangeToVisible:NSMakeRange([[textView string] length], 0)];
}

static void frpRunStatusOpenMain(void *ctx) {
	@autoreleasepool {
		FrpRunStatusOpenMessage *msg = (FrpRunStatusOpenMessage *)ctx;
		frpEnsureRunStatusStore();

		int width = 460;
		int height = 260;
		int offset = msg->slot * 28;
		NSRect screen = [[NSScreen mainScreen] visibleFrame];
		NSRect frame = NSMakeRect(
			NSMaxX(screen) - 100 - width - offset,
			NSMaxY(screen) - 100 - height - offset,
			width,
			height
		);

		NSWindow *window = [[NSWindow alloc]
			initWithContentRect:frame
			styleMask:(NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskFullSizeContentView)
			backing:NSBackingStoreBuffered
			defer:NO];
		[window setTitle:frpRunStatusString(msg->name)];
		[window setLevel:NSFloatingWindowLevel];
		[window setOpaque:NO];
		[window setAlphaValue:0.88];
		[window setBackgroundColor:[NSColor colorWithCalibratedRed:0.08 green:0.09 blue:0.12 alpha:0.88]];
		[window setTitlebarAppearsTransparent:YES];
		[window setMovableByWindowBackground:YES];
		[window setReleasedWhenClosed:YES];
		[window setCollectionBehavior:NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorFullScreenAuxiliary];

		NSView *content = [window contentView];
		[content setWantsLayer:YES];
		[content.layer setBackgroundColor:[[NSColor colorWithCalibratedRed:0.08 green:0.09 blue:0.12 alpha:0.88] CGColor]];

		NSRect contentBounds = [content bounds];
		NSRect scrollFrame = NSMakeRect(0, 0, contentBounds.size.width, contentBounds.size.height - 38);
		NSScrollView *scroll = [[NSScrollView alloc] initWithFrame:scrollFrame];
		[scroll setAutoresizingMask:NSViewWidthSizable | NSViewHeightSizable | NSViewMaxYMargin];
		[scroll setBorderType:NSNoBorder];
		[scroll setHasVerticalScroller:YES];
		[scroll setDrawsBackground:NO];

		FrpRunStatusTextView *textView = [[FrpRunStatusTextView alloc] initWithFrame:[content bounds]];
		textView.frpStatusID = msg->id;
		[textView setEditable:YES];
		[textView setSelectable:YES];
		[textView setDrawsBackground:NO];
		[textView setTextContainerInset:NSMakeSize(14, 14)];
		[textView setAutoresizingMask:NSViewWidthSizable | NSViewHeightSizable];
		[scroll setDocumentView:textView];
		[content addSubview:scroll];

		NSString *header = [NSString stringWithFormat:@"▶ %@\n$ %@\n\n运行中...\n\n",
			frpRunStatusString(msg->name), frpRunStatusString(msg->command)];
		frpAppendToTextView(textView, header, 0, 0);

		NSNumber *key = [NSNumber numberWithInt:msg->id];
		FrpRunStatusDelegate *del = [[FrpRunStatusDelegate alloc] init];
		del.frpStatusID = msg->id;
		[window setDelegate:del];
		[frpRunStatusDelegates setObject:del forKey:key];
		[frpRunStatusOutputStarts setObject:[NSNumber numberWithUnsignedInteger:[[textView string] length]] forKey:key];
		[frpRunStatusWindows setObject:window forKey:key];
		[frpRunStatusViews setObject:textView forKey:key];
		[window makeFirstResponder:textView];
		[window orderFrontRegardless];

		free(msg->name);
		free(msg->command);
		free(msg);
	}
}

static void frpRunStatusAppendMain(void *ctx) {
	@autoreleasepool {
		FrpRunStatusTextMessage *msg = (FrpRunStatusTextMessage *)ctx;
		frpEnsureRunStatusStore();
		NSTextView *textView = [frpRunStatusViews objectForKey:[NSNumber numberWithInt:msg->id]];
		frpAppendToTextView(textView, frpRunStatusString(msg->text), msg->color, msg->bold);
		if (textView != nil) {
			[textView scrollRangeToVisible:NSMakeRange([[textView string] length], 0)];
		}
		free(msg->text);
		free(msg);
	}
}

static void frpRunStatusReplaceCountdownMain(void *ctx) {
	@autoreleasepool {
		FrpRunStatusTextMessage *msg = (FrpRunStatusTextMessage *)ctx;
		frpEnsureRunStatusStore();
		NSTextView *textView = [frpRunStatusViews objectForKey:[NSNumber numberWithInt:msg->id]];
		frpReplaceCountdownLine(msg->id, textView, frpRunStatusString(msg->text));
		free(msg->text);
		free(msg);
	}
}

static void frpRunStatusReplaceOutputMain(void *ctx) {
	@autoreleasepool {
		FrpRunStatusTextMessage *msg = (FrpRunStatusTextMessage *)ctx;
		frpEnsureRunStatusStore();
		NSNumber *key = [NSNumber numberWithInt:msg->id];
		NSTextView *textView = [frpRunStatusViews objectForKey:key];
		NSNumber *startNum = [frpRunStatusOutputStarts objectForKey:key];
		if (textView != nil && startNum != nil) {
			NSUInteger start = [startNum unsignedIntegerValue];
			NSUInteger len = [[textView textStorage] length];
			if (start > len) {
				start = len;
			}
			[[textView textStorage] deleteCharactersInRange:NSMakeRange(start, len - start)];
			frpAppendANSIText(textView, frpRunStatusString(msg->text));
			[textView scrollRangeToVisible:NSMakeRange([[textView string] length], 0)];
		}
		free(msg->text);
		free(msg);
	}
}

static void frpRunStatusCloseMain(void *ctx) {
	@autoreleasepool {
		FrpRunStatusCloseMessage *msg = (FrpRunStatusCloseMessage *)ctx;
		frpEnsureRunStatusStore();
		NSNumber *key = [NSNumber numberWithInt:msg->id];
		NSWindow *window = [frpRunStatusWindows objectForKey:key];
		if (window != nil) {
			[window close];
		}
		free(msg);
	}
}

static void frpRunStatusOpen(int id, int slot, const char *name, const char *command) {
	FrpRunStatusOpenMessage *msg = malloc(sizeof(FrpRunStatusOpenMessage));
	msg->id = id;
	msg->slot = slot;
	msg->name = strdup(name == NULL ? "" : name);
	msg->command = strdup(command == NULL ? "" : command);
	dispatch_async_f(dispatch_get_main_queue(), msg, frpRunStatusOpenMain);
}

static void frpRunStatusAppend(int id, const char *text, int color, int bold) {
	FrpRunStatusTextMessage *msg = malloc(sizeof(FrpRunStatusTextMessage));
	msg->id = id;
	msg->color = color;
	msg->bold = bold;
	msg->text = strdup(text == NULL ? "" : text);
	dispatch_async_f(dispatch_get_main_queue(), msg, frpRunStatusAppendMain);
}

static void frpRunStatusReplaceCountdown(int id, const char *text) {
	FrpRunStatusTextMessage *msg = malloc(sizeof(FrpRunStatusTextMessage));
	msg->id = id;
	msg->color = 0;
	msg->bold = 0;
	msg->text = strdup(text == NULL ? "" : text);
	dispatch_async_f(dispatch_get_main_queue(), msg, frpRunStatusReplaceCountdownMain);
}

static void frpRunStatusReplaceOutput(int id, const char *text) {
	FrpRunStatusTextMessage *msg = malloc(sizeof(FrpRunStatusTextMessage));
	msg->id = id;
	msg->color = 0;
	msg->bold = 0;
	msg->text = strdup(text == NULL ? "" : text);
	dispatch_async_f(dispatch_get_main_queue(), msg, frpRunStatusReplaceOutputMain);
}

static void frpRunStatusClose(int id) {
	FrpRunStatusCloseMessage *msg = malloc(sizeof(FrpRunStatusCloseMessage));
	msg->id = id;
	dispatch_async_f(dispatch_get_main_queue(), msg, frpRunStatusCloseMain);
}
*/
import "C"

import (
	"sync/atomic"
	"time"
	"unsafe"
)

var runStatusSeq uint64

func openRunStatus(name, command string) int {
	id := int(atomic.AddUint64(&runStatusSeq, 1))
	cName := C.CString(name)
	cCommand := C.CString(command)
	defer C.free(unsafe.Pointer(cName))
	defer C.free(unsafe.Pointer(cCommand))
	C.frpRunStatusOpen(C.int(id), C.int((id-1)%statusWindowSlots), cName, cCommand)
	return id
}

func appendRunStatus(id int, text string) {
	if id == 0 || text == "" {
		return
	}
	for _, run := range parseStatusANSIRuns(text) {
		cText := C.CString(run.Text)
		C.frpRunStatusAppend(C.int(id), cText, C.int(run.Color), C.int(boolToInt(run.Bold)))
		C.free(unsafe.Pointer(cText))
	}
}

func replaceRunStatusOutput(id int, text string) {
	if id == 0 {
		return
	}
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cText))
	C.frpRunStatusReplaceOutput(C.int(id), cText)
}

func replaceRunStatusCountdown(id int, text string) {
	if id == 0 || text == "" {
		return
	}
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cText))
	C.frpRunStatusReplaceCountdown(C.int(id), cText)
}

func finishRunStatus(id int, text string, ok bool) {
	flushRunStatusFeed(id)
	closeRunStatusFeed(id)
	if takeRunStatusUserClosed(id) {
		return
	}
	if text == "" {
		if ok {
			text = "\n完成，退出码 0\n"
		} else {
			text = "\n执行失败\n"
		}
	}
	appendRunStatus(id, text)
	if runStatusShouldAutoClose(ok) {
		go closeRunStatusAfterCountdown(id)
	}
}

func closeRunStatusAfterCountdown(id int) {
	for i := 5; i >= 1; i-- {
		replaceRunStatusCountdown(id, statusWindowCloseCountdownLine(i))
		time.Sleep(time.Second)
	}
	C.frpRunStatusClose(C.int(id))
}
