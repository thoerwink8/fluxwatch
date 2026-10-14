// 见 statusitem_darwin.m。
#ifndef FW_STATUSITEM_DARWIN_H
#define FW_STATUSITEM_DARWIN_H

typedef struct {
	int found;    // 找到了 Wails 建的状态项
	int visible;  // NSStatusItem.isVisible
	int dark;     // 菜单栏此刻是暗的
	double x, w;  // 状态项按钮在屏幕坐标里的左边和宽度（点）
	double screenX, screenW;
	int hasNotch; // 这块屏幕有刘海
	double notchL, notchR; // 刘海左右边（屏幕坐标，点）
} fwItemInfo;

void fwSeedStatusPosition(const char *autosave);
int fwStatusItemSetup(const char *autosave);
fwItemInfo fwStatusItemInfo(void);
int fwMenuBarDark(void);
void fwCenterTop(void *win, int offset);
void fwActivate(void);
int fwHotkeyRegister(void);

#endif
