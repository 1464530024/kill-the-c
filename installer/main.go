package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

//go:embed payload/app.gz
var payloadGz []byte

const (
	idChange  = 1001
	idDesktop = 1002
	idInstall = 1003
	idClose   = 1004

	winW = 500
	winH = 316

	colBg      = 0x00FFFFFF
	colText    = 0x001F1D1D
	colSub     = 0x0073736E
	colAccent  = 0x00E37100
	colGroove  = 0x00EEEAE9
	colLine    = 0x00F2F1EF
	colBox     = 0x00F8F7F5
)

type state struct {
	hwnd      uintptr
	pct       int
	status    string
	installing bool
	done      bool
	failed    bool
	dir       string
	desktop   bool

	hFontTitle uintptr
	hFontSub   uintptr
	hFontBody  uintptr
	hFontSmall uintptr
	hFontBtn   uintptr

	hIcon    uintptr
	hChange  uintptr
	hDesktop uintptr
	hInstall uintptr
	hClose   uintptr

	mu sync.Mutex
}

var app state

func main() {
	_, _, _ = pCoInitializeEx.Call(0, 2)
	app.dir = DefaultInstallDir()
	app.desktop = true
	app.status = "准备就绪，点击「开始安装」"

	// 支持静默安装：KillTheC-Setup.exe /S [/D=<目录>]
	silent, sdir := parseSilent()
	if silent {
		if sdir != "" {
			app.dir = sdir
		}
		if err := Install(app.dir, false, payloadGz, func(int, string) {}); err != nil {
			messageBox(0, appDisplayName+" 安装失败", err.Error(), MB_OK|MB_ICONERROR)
			os.Exit(1)
		}
		os.Exit(0)
	}

	runWindow()
}

func parseSilent() (bool, string) {
	silent := false
	var dir string
	for _, a := range os.Args[1:] {
		switch {
		case a == "/S" || a == "/s" || a == "--silent":
			silent = true
		case len(a) > 3 && (a[:3] == "/D=" || a[:3] == "/d="):
			dir = a[3:]
		}
	}
	return silent, dir
}

func runWindow() {
	hInst, _, _ := pGetModuleHandleW.Call(0)
	className := u16(WINCLASS)
	clsIcon, _, _ := pLoadImageW.Call(hInst, 1, 1, 0, 0, 0x8000|0x0040) // IMAGE_ICON, LR_SHARED|LR_DEFAULTSIZE

	wc := wndClassExW{
		CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		Style:         CS_HREDRAW | CS_VREDRAW | CS_DBLCLKS,
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     hInst,
		HIcon:         clsIcon,
		HIconSm:       clsIcon,
		HCursor:       mustCursor(),
		HbrBackground: 0,
		LpszClassName: className,
	}
	if r, _, _ := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		messageBox(0, appDisplayName, "注册窗口类失败", MB_OK|MB_ICONERROR)
		return
	}

	sx, _, _ := pGetSystemMetrics.Call(0)
	sy, _, _ := pGetSystemMetrics.Call(1)
	rc := rect{0, 0, winW, winH}
	pAdjustWindowRect.Call(uintptr(unsafe.Pointer(&rc)), WS_CAPTION|WS_SYSMENU|WS_MINIMIZEBOX, 0)
	ww := rc.Right - rc.Left
	wh := rc.Bottom - rc.Top
	x := (int32(sx) - ww) / 2
	y := (int32(sy) - wh) / 2

	hwnd, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)),
		ui16(appDisplayName+" 安装向导"),
		WS_OVERLAPPED|WS_CAPTION|WS_SYSMENU|WS_MINIMIZEBOX,
		uintptr(x), uintptr(y), uintptr(ww), uintptr(wh),
		0, 0, hInst, 0)
	if hwnd == 0 {
		messageBox(0, appDisplayName, "创建窗口失败", MB_OK|MB_ICONERROR)
		return
	}
	app.hwnd = hwnd
	pShowWindow.Call(hwnd, SW_SHOW)
	pUpdateWindow.Call(hwnd)
	pSetForegroundWindow.Call(hwnd)

	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func mustCursor() uintptr {
	c, _, _ := user32.NewProc("LoadCursorW").Call(0, IDC_ARROW)
	return c
}

// ── 窗口过程 ──────────────────────────────────────────────────────────────

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case WM_CREATE:
		createFonts()
		createControls(hwnd)
		return 0
	case WM_PAINT:
		paint(hwnd)
		return 0
	case WM_ERASEBKGND:
		return 1
	case WM_COMMAND:
		onCommand(hwnd, wp)
		return 0
	case WM_APP_PROGRESS:
		app.mu.Lock()
		app.pct = int(wp)
		app.mu.Unlock()
		pInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case WM_APP_STATUS:
		pInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case WM_APP_DONE:
		finish(hwnd, int32(lp) != 0)
		return 0
	case WM_CLOSE:
		if app.installing {
			return 0
		}
		pDestroyWindow.Call(hwnd)
		return 0
	case WM_DESTROY:
		if app.hIcon != 0 {
			user32.NewProc("DestroyIcon").Call(app.hIcon)
		}
		freeFonts()
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func createFonts() {
	mk := func(h int, weight uint32, name string) uintptr {
		f, _, _ := pCreateFontW.Call(
			uintptr(int32(h)), 0, 0, 0, uintptr(weight), 0, 0, 0,
			DEFAULT_CHARSET, 0, 0, CLEARTYPE_QUALITY, 0,
			ui16(name))
		return f
	}
	face := "Microsoft YaHei UI"
	app.hFontTitle = mk(-27, FW_SEMIBOLD, face)
	app.hFontSub = mk(-16, FW_NORMAL, face)
	app.hFontBody = mk(-16, FW_NORMAL, face)
	app.hFontSmall = mk(-15, FW_NORMAL, face)
	app.hFontBtn = mk(-16, FW_NORMAL, face)
}

func freeFonts() {
	for _, f := range []uintptr{app.hFontTitle, app.hFontSub, app.hFontBody, app.hFontSmall, app.hFontBtn} {
		if f != 0 {
			pDeleteObject.Call(f)
		}
	}
}

func mkButton(parent uintptr, text string, x, y, w, hh int, id int) uintptr {
	hInst, _, _ := pGetModuleHandleW.Call(0)
	ctl, _, _ := pCreateWindowExW.Call(0, ui16("BUTTON"), ui16(text),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON,
		uintptr(x), uintptr(y), uintptr(w), uintptr(hh),
		parent, uintptr(id), hInst, 0)
	pSendMessageW.Call(ctl, WM_SETFONT, app.hFontBtn, 1)
	return ctl
}

func createControls(hwnd uintptr) {
	hInst, _, _ := pGetModuleHandleW.Call(0)
	// 按目标像素尺寸单独加载，不用共享缓存（共享图标会忽略尺寸参数）
	app.hIcon, _, _ = pLoadImageW.Call(hInst, 1, 1, 48, 48, 0)
	app.hChange = mkButton(hwnd, "更改…", 392, 136, 80, 27, idChange)
	app.hInstall = mkButton(hwnd, "开始安装", 292, 258, 92, 32, idInstall)
	app.hClose = mkButton(hwnd, "关闭", 392, 258, 80, 32, idClose)

	chk, _, _ := pCreateWindowExW.Call(0, ui16("BUTTON"), ui16("创建桌面快捷方式"),
		WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_AUTOCHECKBOX,
		26, 176, 220, 20, hwnd, uintptr(idDesktop), hInst, 0)
	pSendMessageW.Call(chk, WM_SETFONT, app.hFontSmall, 1)
	pSendMessageW.Call(chk, 0x00F1, 1, 0) // BM_SETCHECK = 1
	app.hDesktop = chk
}

func onCommand(hwnd, wp uintptr) {
	id := int(wp & 0xFFFF)
	switch id {
	case idChange:
		if app.installing || app.done {
			return
		}
		if d := pickFolder(hwnd, app.dir); d != "" {
			app.dir = filepath.Join(d, "KillTheC")
			pInvalidateRect.Call(hwnd, 0, 0)
		}
	case idDesktop:
		app.desktop = !app.desktop
	case idInstall:
		if app.done {
			launchApp(filepath.Join(app.dir, appExeName))
			pDestroyWindow.Call(hwnd)
			return
		}
		if app.installing {
			return
		}
		startInstall(hwnd)
	case idClose:
		if app.installing {
			return
		}
		pDestroyWindow.Call(hwnd)
	}
}

func startInstall(hwnd uintptr) {
	app.installing = true
	pEnableWindow.Call(app.hInstall, 0)
	pEnableWindow.Call(app.hChange, 0)
	pEnableWindow.Call(app.hDesktop, 0)
	pSetWindowTextW.Call(app.hClose, ui16("取消"))
	pInvalidateRect.Call(hwnd, 0, 0)

	go func() {
		report := func(pct int, status string) {
			app.mu.Lock()
			app.pct = pct
			app.status = status
			app.mu.Unlock()
			pPostMessageW.Call(hwnd, WM_APP_PROGRESS, uintptr(pct), 0)
		}
		err := Install(app.dir, app.desktop, payloadGz, report)
		var code uintptr
		if err != nil {
			app.mu.Lock()
			app.status = "安装失败：" + err.Error()
			app.mu.Unlock()
			code = 1
		}
		pPostMessageW.Call(hwnd, WM_APP_DONE, 0, code)
	}()
}

func finish(hwnd uintptr, failed bool) {
	app.installing = false
	app.done = true
	app.failed = failed
	if !failed {
		app.mu.Lock()
		app.status = "安装完成，程序已就绪"
		app.mu.Unlock()
		pSetWindowTextW.Call(app.hInstall, ui16("立即运行"))
		pSetWindowTextW.Call(app.hClose, ui16("完成"))
		pEnableWindow.Call(app.hInstall, 1)
	} else {
		pSetWindowTextW.Call(app.hClose, ui16("关闭"))
		pEnableWindow.Call(app.hChange, 1)
	}
	pEnableWindow.Call(app.hClose, 1)
	pInvalidateRect.Call(hwnd, 0, 0)
}

// ── 绘制 ──────────────────────────────────────────────────────────────────

func paint(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))

	var cr rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr)))

	bg, _, _ := pCreateSolidBrush.Call(colBg)
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&cr)), bg)
	pDeleteObject.Call(bg)

	pSetBkMode.Call(hdc, TRANSPARENT)

	// 图标（按图标自身尺寸绘制，避免缩放错位）
	if app.hIcon != 0 {
		pDrawIconEx.Call(hdc, 26, 22, app.hIcon, 0, 0, 0, 0, 3) // DI_NORMAL
	}

	// 标题 / 副标题
	pSelectObject.Call(hdc, app.hFontTitle)
	pSetTextColor.Call(hdc, colText)
	drawText(hdc, appDisplayName, rect{92, 24, 470, 56}, DT_LEFT|DT_SINGLELINE|DT_VCENTER)
	pSelectObject.Call(hdc, app.hFontSub)
	pSetTextColor.Call(hdc, colSub)
	drawText(hdc, "磁盘清理专家 · 安装向导", rect{92, 56, 470, 76}, DT_LEFT|DT_SINGLELINE|DT_VCENTER)

	// 分隔线
	line, _, _ := pCreateSolidBrush.Call(colLine)
	lr := rect{26, 96, 474, 97}
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&lr)), line)
	pDeleteObject.Call(line)

	// 安装位置
	pSelectObject.Call(hdc, app.hFontSmall)
	pSetTextColor.Call(hdc, colSub)
	drawText(hdc, "安装位置", rect{26, 112, 200, 130}, DT_LEFT|DT_SINGLELINE|DT_VCENTER)

	box, _, _ := pCreateSolidBrush.Call(colBox)
	br := rect{26, 137, 382, 163}
	roundFill(hdc, br, 6, box)
	pSelectObject.Call(hdc, app.hFontBody)
	pSetTextColor.Call(hdc, colText)
	drawText(hdc, app.dir, rect{36, 137, 374, 163}, DT_LEFT|DT_SINGLELINE|DT_VCENTER|DT_END_ELLIPSIS)

	// 进度槽
	groove, _, _ := pCreateSolidBrush.Call(colGroove)
	gr := rect{26, 212, 474, 222}
	roundFill(hdc, gr, 10, groove)

	app.mu.Lock()
	pct := app.pct
	status := app.status
	app.mu.Unlock()

	if pct > 0 {
		w := int32(448 * pct / 100)
		if w < 8 {
			w = 8
		}
		bar, _, _ := pCreateSolidBrush.Call(colAccent)
		brr := rect{26, 212, 26 + w, 222}
		roundFill(hdc, brr, 10, bar)
		pDeleteObject.Call(bar)
	}

	// 状态文字
	pSelectObject.Call(hdc, app.hFontSmall)
	if app.failed {
		pSetTextColor.Call(hdc, 0x002A1AFF)
	} else {
		pSetTextColor.Call(hdc, colSub)
	}
	drawText(hdc, status, rect{26, 230, 380, 250}, DT_LEFT|DT_SINGLELINE|DT_VCENTER|DT_END_ELLIPSIS)

	// 右侧百分比
	if app.done {
		pSetTextColor.Call(hdc, colAccent)
		drawText(hdc, "100%", rect{390, 230, 474, 250}, DT_CENTER|DT_SINGLELINE|DT_VCENTER)
	} else if app.installing || pct > 0 {
		pSetTextColor.Call(hdc, colText)
		drawText(hdc, fmt.Sprintf("%d%%", pct), rect{390, 230, 474, 250}, DT_CENTER|DT_SINGLELINE|DT_VCENTER)
	}
}

// roundFill 用 RoundRect + 空画笔填充圆角矩形。
// 注意：不要用 SelectObject 选入 HRGN，那会残留裁剪区并把后续绘制裁掉。
func roundFill(hdc uintptr, r rect, radius int32, brush uintptr) {
	nullPen, _, _ := pGetStockObject.Call(8) // NULL_PEN
	oldBrush, _, _ := pSelectObject.Call(hdc, brush)
	oldPen, _, _ := pSelectObject.Call(hdc, nullPen)
	pRoundRect.Call(hdc,
		uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom),
		uintptr(radius), uintptr(radius))
	pSelectObject.Call(hdc, oldBrush)
	pSelectObject.Call(hdc, oldPen)
}

func drawText(hdc uintptr, text string, r rect, flags uint32) {
	t := utf16Ptr(text)
	pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(t)), ^uintptr(0),
		uintptr(unsafe.Pointer(&r)), uintptr(flags))
}

func utf16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

var _ = bytes.MinRead
