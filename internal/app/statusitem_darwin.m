//go:build darwin

// Wails v3 没暴露 NSStatusItem、NSScreen 的刘海区和全局快捷键，这里补上：
//   - 找到 Wails 建的状态项（按钮的 target 是 Wails 的 StatusItemController，它有 statusItem 属性），
//     设 autosaveName（⌘ 拖过的位置下次还在），盯按钮的 effectiveAppearance（菜单栏明暗变了重画图标）；
//   - 用 auxiliaryTopLeftArea / auxiliaryTopRightArea 判断状态项是否落进刘海；
//   - Carbon RegisterEventHotKey 注册 ⌥⌘F（不需要辅助功能权限）。
// 这些函数都要在主线程调（Go 侧经 application.InvokeSync 进来）。

#import <Cocoa/Cocoa.h>
#import <Carbon/Carbon.h>
#include "statusitem_darwin.h"

extern void fwGoMenuBarAppearance(int dark);
extern void fwGoHotkey(void);

static NSStatusItem *fwItem = nil;

static NSStatusBarButton *fwFindButton(NSView *v) {
	if (v == nil) return nil;
	if ([v isKindOfClass:[NSStatusBarButton class]]) return (NSStatusBarButton *)v;
	for (NSView *s in v.subviews) {
		NSStatusBarButton *b = fwFindButton(s);
		if (b) return b;
	}
	return nil;
}

// fwFindItem 在本进程的窗口里找 Wails 建的那只状态项。本应用只有一只托盘。
static NSStatusItem *fwFindItem(void) {
	if (fwItem != nil) return fwItem;
	Class ctl = NSClassFromString(@"StatusItemController");
	if (ctl == nil) return nil;
	for (NSWindow *w in [NSApp windows]) {
		NSStatusBarButton *b = fwFindButton(w.contentView);
		if (b == nil) continue;
		id target = b.target;
		if (target == nil || ![target isKindOfClass:ctl]) continue;
		id item = [target valueForKey:@"statusItem"];
		if ([item isKindOfClass:[NSStatusItem class]]) {
			fwItem = (NSStatusItem *)item;
			return fwItem;
		}
	}
	return nil;
}

static int fwIsDark(NSAppearance *a) {
	if (a == nil) return 0;
	if (@available(macOS 10.14, *)) {
		NSAppearanceName n = [a bestMatchFromAppearancesWithNames:@[ NSAppearanceNameAqua, NSAppearanceNameDarkAqua ]];
		return [n isEqualToString:NSAppearanceNameDarkAqua] ? 1 : 0;
	}
	return 0; // 10.13 没有暗色菜单栏
}

@interface FWAppearanceObserver : NSObject
@end

@implementation FWAppearanceObserver
- (void)observeValueForKeyPath:(NSString *)keyPath ofObject:(id)object change:(NSDictionary *)change context:(void *)context {
	NSView *v = (NSView *)object;
	int dark = fwIsDark(v.effectiveAppearance);
	fwGoMenuBarAppearance(dark);
}
@end

static FWAppearanceObserver *fwObserver = nil;

// 菜单栏挤的时候系统把新状态项放在最左，正好落进刘海。状态项建好前往 defaults 里写一个靠右的首选位置
// （点，离屏幕右边的距离）：刘海右边缘再往右 56 点；有刘海才写，用户已有的位置（⌘ 拖过的）不动。
// Wails 建的状态项没名字，系统叫它 Item-0，建的时候读这个键；之后我们改 autosaveName，那个键也一并写上。
void fwSeedStatusPosition(const char *autosave) {
	if (@available(macOS 12.0, *)) {
		for (NSScreen *s in [NSScreen screens]) {
			NSRect l = s.auxiliaryTopLeftArea, r = s.auxiliaryTopRightArea;
			if (NSIsEmptyRect(l) || NSIsEmptyRect(r)) continue;
			CGFloat ox = (l.origin.x == s.frame.origin.x) ? 0 : s.frame.origin.x;
			CGFloat notchR = NSMinX(r) + ox - s.frame.origin.x;
			double pos = s.frame.size.width - notchR - 56;
			if (pos < 100) return;
			NSUserDefaults *d = [NSUserDefaults standardUserDefaults];
			NSString *k1 = @"NSStatusItem Preferred Position Item-0";
			NSString *k2 = [NSString stringWithFormat:@"NSStatusItem Preferred Position %s", autosave];
			if ([d objectForKey:k1] == nil) [d setDouble:pos forKey:k1];
			if ([d objectForKey:k2] == nil) [d setDouble:pos forKey:k2];
			return;
		}
	}
}

int fwStatusItemSetup(const char *autosave) {
	NSStatusItem *item = fwFindItem();
	if (item == nil) return 0;
	if (autosave != NULL && item.autosaveName.length == 0) {
		item.autosaveName = [NSString stringWithUTF8String:autosave];
	}
	if (fwObserver == nil && item.button != nil) {
		fwObserver = [[FWAppearanceObserver alloc] init];
		[item.button addObserver:fwObserver forKeyPath:@"effectiveAppearance" options:NSKeyValueObservingOptionNew context:NULL];
	}
	return 1;
}

static NSScreen *fwScreenFor(NSWindow *w) {
	NSScreen *s = w.screen;
	if (s == nil) s = [NSScreen mainScreen];
	return s;
}

fwItemInfo fwStatusItemInfo(void) {
	fwItemInfo out = {0};
	NSStatusItem *item = fwFindItem();
	if (item == nil || item.button == nil || item.button.window == nil) return out;
	NSStatusBarButton *b = item.button;
	NSRect f = [b.window convertRectToScreen:[b convertRect:b.bounds toView:nil]];
	NSScreen *s = fwScreenFor(b.window);
	out.found = 1;
	out.visible = item.isVisible ? 1 : 0;
	out.dark = fwIsDark(b.effectiveAppearance);
	out.x = f.origin.x;
	out.w = f.size.width;
	if (s != nil) {
		out.screenX = s.frame.origin.x;
		out.screenW = s.frame.size.width;
		if (@available(macOS 12.0, *)) {
			NSRect l = s.auxiliaryTopLeftArea, r = s.auxiliaryTopRightArea;
			if (!NSIsEmptyRect(l) && !NSIsEmptyRect(r)) {
				// 两块「刘海左右没被挡住的区域」之间就是刘海。文档没说是屏幕坐标还是屏幕内坐标，两种都兜住。
				CGFloat ox = (l.origin.x == s.frame.origin.x) ? 0 : s.frame.origin.x;
				out.hasNotch = 1;
				out.notchL = NSMaxX(l) + ox;
				out.notchR = NSMinX(r) + ox;
			}
		}
	}
	return out;
}

int fwMenuBarDark(void) {
	NSStatusItem *item = fwFindItem();
	if (item == nil || item.button == nil) return -1;
	return fwIsDark(item.button.effectiveAppearance);
}

void fwCenterTop(void *win, int offset) {
	NSWindow *w = (NSWindow *)win;
	if (w == nil) return;
	// 鼠标所在的屏幕；找不到用主屏
	NSPoint m = [NSEvent mouseLocation];
	NSScreen *s = nil;
	for (NSScreen *c in [NSScreen screens]) {
		if (NSPointInRect(m, c.frame)) { s = c; break; }
	}
	if (s == nil) s = [NSScreen mainScreen];
	if (s == nil) return;
	NSRect vf = s.visibleFrame, wf = w.frame;
	CGFloat scaled = offset * s.backingScaleFactor; // 和 Wails 贴托盘时的间距一致
	wf.origin.x = NSMidX(s.frame) - wf.size.width / 2;
	wf.origin.y = NSMaxY(vf) - wf.size.height - scaled;
	[w setFrame:wf display:YES animate:NO];
}

void fwActivate(void) {
	if (@available(macOS 14.0, *)) {
		[NSApp activate];
	} else {
		[NSApp activateIgnoringOtherApps:YES];
	}
}

static OSStatus fwHotkeyHandler(EventHandlerCallRef next, EventRef ev, void *ud) {
	fwGoHotkey();
	return noErr;
}

static EventHotKeyRef fwHotkeyRef = NULL;

int fwHotkeyRegister(void) {
	if (fwHotkeyRef != NULL) return 0;
	EventTypeSpec spec = {kEventClassKeyboard, kEventHotKeyPressed};
	OSStatus st = InstallApplicationEventHandler(&fwHotkeyHandler, 1, &spec, NULL, NULL);
	if (st != noErr) return (int)st;
	EventHotKeyID hid = {'FLXW', 1};
	return (int)RegisterEventHotKey(kVK_ANSI_F, cmdKey | optionKey, hid, GetApplicationEventTarget(), 0, &fwHotkeyRef);
}
