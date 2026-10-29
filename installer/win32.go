// Package main —— Kill the C 安装程序（纯 Win32 原生窗口，无外部运行时依赖）。
package main

import (
	"syscall"
	"unsafe"
)

const (
	WINCLASS = "KillTheCSetupWnd"

	WS_OVERLAPPED   = 0x00000000
	WS_CAPTION      = 0x00C00000
	WS_SYSMENU      = 0x00080000
	WS_MINIMIZEBOX  = 0x00020000
	WS_VISIBLE      = 0x10000000
	WS_CHILD        = 0x40000000
	WS_TABSTOP      = 0x00010000
	WS_BORDER       = 0x00800000
	BS_PUSHBUTTON   = 0x00000000
	BS_AUTOCHECKBOX = 0x00000003
	BS_OWNERDRAW    = 0x0000000B
	SS_LEFT         = 0x00000000
	CS_HREDRAW      = 0x0002
	CS_VREDRAW      = 0x0001
	CS_DBLCLKS      = 0x0008

	SW_SHOW      = 5
	SW_SHOWNORMAL = 1

	WM_CREATE       = 0x0001
	WM_DESTROY      = 0x0002
	WM_CLOSE        = 0x0010
	WM_PAINT        = 0x000F
	WM_ERASEBKGND   = 0x0014
	WM_COMMAND      = 0x0111
	WM_SETFONT      = 0x0030
	WM_LBUTTONDOWN  = 0x0201
	WM_LBUTTONUP    = 0x0202
	WM_MOUSEMOVE    = 0x0200
	WM_APP          = 0x8000
	WM_APP_PROGRESS = WM_APP + 1
	WM_APP_STATUS   = WM_APP + 2
	WM_APP_DONE     = WM_APP + 3

	IDC_ARROW   = 32512
	IDI_APPLICATION = 32512

	COLOR_WINDOW = 5

	DT_LEFT     = 0x00000000
	DT_CENTER   = 0x00000001
	DT_VCENTER  = 0x00000004
	DT_SINGLELINE = 0x00000020
	DT_END_ELLIPSIS = 0x00008000

	FW_SEMIBOLD = 600
	FW_NORMAL   = 400

	DEFAULT_CHARSET = 1
	CLEARTYPE_QUALITY = 5
	ANTIALIASED_QUALITY = 4

	TRANSPARENT = 1

	GWLP_USERDATA = -21

	MB_OK              = 0x00000000
	MB_ICONINFORMATION = 0x00000040
	MB_ICONERROR       = 0x00000010
	MB_YESNO           = 0x00000004
	IDYES              = 6
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")

	pRegisterClassExW    = user32.NewProc("RegisterClassExW")
	pCreateWindowExW     = user32.NewProc("CreateWindowExW")
	pDefWindowProcW      = user32.NewProc("DefWindowProcW")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pPostQuitMessage     = user32.NewProc("PostQuitMessage")
	pShowWindow          = user32.NewProc("ShowWindow")
	pUpdateWindow        = user32.NewProc("UpdateWindow")
	pGetMessageW         = user32.NewProc("GetMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pDispatchMessageW    = user32.NewProc("DispatchMessageW")
	pPostMessageW        = user32.NewProc("PostMessageW")
	pSendMessageW        = user32.NewProc("SendMessageW")
	pBeginPaint          = user32.NewProc("BeginPaint")
	pEndPaint            = user32.NewProc("EndPaint")
	pInvalidateRect      = user32.NewProc("InvalidateRect")
	pGetClientRect       = user32.NewProc("GetClientRect")
	pSetWindowTextW      = user32.NewProc("SetWindowTextW")
	pGetWindowTextW      = user32.NewProc("GetWindowTextW")
	pGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	pSetWindowPos        = user32.NewProc("SetWindowPos")
	pAdjustWindowRect    = user32.NewProc("AdjustWindowRect")
	pGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
	pEnableWindow        = user32.NewProc("EnableWindow")
	pGetDlgItem          = user32.NewProc("GetDlgItem")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pMessageBoxW         = user32.NewProc("MessageBoxW")
	pLoadImageW          = user32.NewProc("LoadImageW")
	pDrawIconEx          = user32.NewProc("DrawIconEx")
	pShellExecuteW       = shell32.NewProc("ShellExecuteW")
	pSHBrowseForFolderW  = shell32.NewProc("SHBrowseForFolderW")
	pSHGetPathFromIDListW = shell32.NewProc("SHGetPathFromIDListW")
	pCoInitializeEx      = ole32.NewProc("CoInitializeEx")
	pCoCreateInstance    = ole32.NewProc("CoCreateInstance")
	pCoUninitialize      = ole32.NewProc("CoUninitialize")

	pCreateSolidBrush   = gdi32.NewProc("CreateSolidBrush")
	pCreateFontW        = gdi32.NewProc("CreateFontW")
	pSelectObject       = gdi32.NewProc("SelectObject")
	pDeleteObject       = gdi32.NewProc("DeleteObject")
	pSetTextColor       = gdi32.NewProc("SetTextColor")
	pSetBkMode          = gdi32.NewProc("SetBkMode")
	pFillRect           = user32.NewProc("FillRect")
	pDrawTextW          = user32.NewProc("DrawTextW")
	pCreateRoundRectRgn = gdi32.NewProc("CreateRoundRectRgn")
	pRoundRect          = gdi32.NewProc("RoundRect")
	pGetStockObject     = gdi32.NewProc("GetStockObject")
)

func u16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func ui16(s string) uintptr { return uintptr(unsafe.Pointer(u16(s))) }

type rect struct{ Left, Top, Right, Bottom int32 }

type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type paintStruct struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     rect
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

type browseInfo struct {
	HwndOwner      uintptr
	PidlRoot       uintptr
	PszDisplayName *uint16
	LpszTitle      *uint16
	UlFlags        uint32
	Lpfn           uintptr
	LParam         uintptr
	IImage         int32
}
