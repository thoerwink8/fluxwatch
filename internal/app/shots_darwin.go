//go:build shots

package app

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

static WKWebView* fwFindWebView(NSView* v) {
	if ([v isKindOfClass:[WKWebView class]]) return (WKWebView*)v;
	for (NSView* s in v.subviews) {
		WKWebView* w = fwFindWebView(s);
		if (w) return w;
	}
	return nil;
}

// fwSnapshot 在主线程拍窗口里 WKWebView 的内容，写成 PNG；失败写 <path>.err。
static void fwSnapshot(void* win, const char* cpath) {
	NSString* path = [NSString stringWithUTF8String:cpath];
	dispatch_async(dispatch_get_main_queue(), ^{
		NSWindow* w = (NSWindow*)win;
		WKWebView* wk = fwFindWebView(w.contentView);
		NSString* errPath = [path stringByAppendingString:@".err"];
		if (!wk) {
			[@"no WKWebView" writeToFile:errPath atomically:YES encoding:NSUTF8StringEncoding error:nil];
			return;
		}
		WKSnapshotConfiguration* cfg = [[WKSnapshotConfiguration alloc] init];
		[wk takeSnapshotWithConfiguration:cfg completionHandler:^(NSImage* img, NSError* err) {
			if (!img) {
				[[err description] writeToFile:errPath atomically:YES encoding:NSUTF8StringEncoding error:nil];
				return;
			}
			CGImageRef cg = [img CGImageForProposedRect:NULL context:nil hints:nil];
			NSBitmapImageRep* rep = [[NSBitmapImageRep alloc] initWithCGImage:cg];
			NSData* d = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
			[d writeToFile:path atomically:YES];
		}];
	});
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const shotsAvailable = true

// snapshotWindow 拍一张窗口里的网页内容（真 WebKit 渲染，不含窗口后面的系统毛玻璃）。
func snapshotWindow(w *application.WebviewWindow, path string) error {
	_ = os.Remove(path)
	_ = os.Remove(path + ".err")
	ptr := w.NativeWindow()
	if ptr == nil {
		return errors.New("拿不到原生窗口")
	}
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	C.fwSnapshot(ptr, cpath)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path + ".err"); err == nil {
			return fmt.Errorf("截图失败：%s", b)
		}
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("截图超时")
}
