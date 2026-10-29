// Package winapi 封装 Kill the C 所需的最小 Win32 能力（全部通过 syscall，零 cgo）。
package winapi

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func execCommand(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return c
}

var (
	modShell32  = windows.NewLazySystemDLL("shell32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modOle32    = windows.NewLazySystemDLL("ole32.dll")

	procSHFileOperationW    = modShell32.NewProc("SHFileOperationW")
	procSHEmptyRecycleBinW  = modShell32.NewProc("SHEmptyRecycleBinW")
	procSHQueryRecycleBinW  = modShell32.NewProc("SHQueryRecycleBinW")
	procShellExecuteW       = modShell32.NewProc("ShellExecuteW")
	procGetDiskFreeSpaceExW = modKernel32.NewProc("GetDiskFreeSpaceExW")
	procDeleteFileW         = modKernel32.NewProc("DeleteFileW")
	procRemoveDirectoryW    = modKernel32.NewProc("RemoveDirectoryW")
	procSetFileAttributesW  = modKernel32.NewProc("SetFileAttributesW")
	procMoveFileExW         = modKernel32.NewProc("MoveFileExW")
	procMessageBoxW         = modUser32.NewProc("MessageBoxW")
	procCoInitializeEx      = modOle32.NewProc("CoInitializeEx")
	procGetVolumeInformationW = modKernel32.NewProc("GetVolumeInformationW")
)

const (
	FO_DELETE = 0x0003

	FOF_MULTIDESTFILES = 0x0001
	FOF_NOCONFIRMATION = 0x0010
	FOF_SILENT         = 0x0004
	FOF_ALLOWUNDO      = 0x0040
	FOF_NOCONFIRMMKDIR = 0x0200
	FOF_NOERRORUI      = 0x0400

	SHERB_NOCONFIRMATION = 0x00000001
	SHERB_NOPROGRESSUI   = 0x00000002
	SHERB_NOSOUND        = 0x00000004

	SW_SHOWNORMAL = 1

	MB_OK              = 0x00000000
	MB_ICONINFORMATION = 0x00000040
	MB_ICONERROR       = 0x00000010

	MOVEFILE_DELAY_UNTIL_REBOOT = 0x00000004

	FILE_ATTRIBUTE_NORMAL         = 0x00000080
	FILE_ATTRIBUTE_REPARSE_POINT  = 0x00000400
	INVALID_FILE_ATTRIBUTES       = 0xFFFFFFFF
)

type shFileOpStructW struct {
	Hwnd           uintptr
	WFunc          uint32
	PFrom          *uint16
	PTo            *uint16
	FFlags         uint16
	FAnyOperations uint32
	HNameMappings  uintptr
	ProgressTitle  *uint16
}

type shQueryRBInfo struct {
	CbSize    uint32
	ISize     int64
	INumItems int64
}

var (
	comOnce sync.Once
)

// InitCOM 在进程启动时调用一次，保证 Shell API 可用。
func InitCOM() {
	comOnce.Do(func() {
		// COINIT_APARTMENTTHREADED = 0x2
		_, _, _ = procCoInitializeEx.Call(0, 2)
	})
}

// IsAdmin 判断当前进程是否具备管理员权限。
func IsAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// ExePath 返回当前可执行文件的绝对路径。
func ExePath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

// ── 长路径处理 ────────────────────────────────────────────────────────────

func toLongPath(p string) string {
	p = filepath.Clean(p)
	p = strings.ReplaceAll(p, "/", `\`)
	if strings.HasPrefix(p, `\\?\`) {
		return p
	}
	if strings.HasPrefix(p, `\\`) {
		return `\\?\UNC\` + p[2:]
	}
	if len(p) >= 240 {
		return `\\?\` + p
	}
	return p
}

// ── 删除：回收站 ──────────────────────────────────────────────────────────

// ToRecycleBin 把一批文件/文件夹送入回收站（可撤销）。
// 返回成功数量与失败路径列表。
func ToRecycleBin(paths []string) (int, []string, error) {
	var ok int
	var failed []string
	for _, chunk := range chunkStrings(paths, 64) {
		n, bad, err := recycleBatch(chunk)
		ok += n
		if len(bad) > 0 {
			failed = append(failed, bad...)
		}
		if err != nil && n == 0 && len(failed) == len(paths) {
			return ok, failed, err
		}
	}
	return ok, failed, nil
}

func recycleBatch(paths []string) (int, []string, error) {
	buf := make([]uint16, 0, 512)
	usable := make([]string, 0, len(paths))
	bad := make([]string, 0)

	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			bad = append(bad, p)
			continue
		}
		abs = filepath.Clean(abs)
		// SHFileOperation 不支持 \\?\ 前缀与超长路径
		if strings.HasPrefix(abs, `\\?\`) {
			abs = abs[4:]
		}
		if len(abs) >= 256 {
			bad = append(bad, p)
			continue
		}
		w, err := syscall.UTF16FromString(abs)
		if err != nil {
			bad = append(bad, p)
			continue
		}
		buf = append(buf, w...)
		usable = append(usable, abs)
	}
	if len(usable) == 0 {
		return 0, bad, nil
	}
	buf = append(buf, 0) // 双 NUL 结尾

	op := &shFileOpStructW{
		WFunc:  FO_DELETE,
		PFrom:  &buf[0],
		FFlags: FOF_ALLOWUNDO | FOF_NOCONFIRMATION | FOF_SILENT | FOF_NOERRORUI | FOF_NOCONFIRMMKDIR,
	}
	ret, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(op)))
	runtime.KeepAlive(buf)

	if ret != 0 {
		// 整批失败时逐条降级，尽量保住能删的
		if len(usable) > 1 {
			var ok int
			for _, one := range usable {
				n, b, _ := recycleBatch([]string{one})
				ok += n
				bad = append(bad, b...)
			}
			return ok, bad, nil
		}
		bad = append(bad, usable...)
		return 0, bad, fmt.Errorf("SHFileOperation 返回错误码 %d", ret)
	}
	if op.FAnyOperations != 0 {
		bad = append(bad, usable...)
		return 0, bad, errors.New("回收站操作被系统中止")
	}
	return len(usable), bad, nil
}

// ── 删除：永久 ────────────────────────────────────────────────────────────

// DeletePermanent 永久删除文件或目录树（不可恢复）。返回删除的文件数。
func DeletePermanent(target string) (int, error) {
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !info.IsDir() {
		if err := deleteFileForce(target); err != nil {
			return 0, err
		}
		return 1, nil
	}

	var n int
	var firstErr error
	_ = filepath.WalkDir(toLongPath(target), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if e := deleteFileForce(p); e != nil {
			if firstErr == nil {
				firstErr = e
			}
			return nil
		}
		n++
		return nil
	})
	_ = removeDirRecursive(toLongPath(target))
	return n, firstErr
}

func deleteFileForce(p string) error {
	lp := toLongPath(p)
	ptr, err := syscall.UTF16PtrFromString(lp)
	if err != nil {
		return err
	}
	r, _, callErr := procDeleteFileW.Call(uintptr(unsafe.Pointer(ptr)))
	if r != 0 {
		return nil
	}
	errno := errnoOf(callErr)
	switch errno {
	case syscall.ERROR_FILE_NOT_FOUND, syscall.ERROR_PATH_NOT_FOUND:
		return nil
	case syscall.ERROR_ACCESS_DENIED:
		// 只读属性导致，清属性后重试一次
		procSetFileAttributesW.Call(uintptr(unsafe.Pointer(ptr)), FILE_ATTRIBUTE_NORMAL)
		r2, _, e2 := procDeleteFileW.Call(uintptr(unsafe.Pointer(ptr)))
		if r2 != 0 {
			return nil
		}
		return fmt.Errorf("DeleteFile(%s): %v", p, errnoOf(e2))
	default:
		return fmt.Errorf("DeleteFile(%s): %v", p, errno)
	}
}

// removeDirRecursive 自底向上删除空目录，返回首个错误。
func removeDirRecursive(root string) error {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	var firstErr error
	for i := len(dirs) - 1; i >= 0; i-- {
		ptr, err := syscall.UTF16PtrFromString(dirs[i])
		if err != nil {
			continue
		}
		r, _, callErr := procRemoveDirectoryW.Call(uintptr(unsafe.Pointer(ptr)))
		if r == 0 {
			errno := errnoOf(callErr)
			if errno == syscall.ERROR_PATH_NOT_FOUND || errno == syscall.ERROR_FILE_NOT_FOUND {
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("RemoveDirectory(%s): %v", dirs[i], errno)
			}
		}
	}
	return firstErr
}

func errnoOf(e error) syscall.Errno {
	var errno syscall.Errno
	if errors.As(e, &errno) {
		return errno
	}
	return 0
}

func chunkStrings(in []string, n int) [][]string {
	var out [][]string
	for i := 0; i < len(in); i += n {
		j := i + n
		if j > len(in) {
			j = len(in)
		}
		out = append(out, in[i:j])
	}
	return out
}

// ── 回收站 ────────────────────────────────────────────────────────────────

// EmptyRecycleBin 静默清空回收站。
func EmptyRecycleBin() error {
	InitCOM()
	ret, _, _ := procSHEmptyRecycleBinW.Call(0, 0,
		SHERB_NOCONFIRMATION|SHERB_NOPROGRESSUI|SHERB_NOSOUND)
	// 0 = S_OK，1 = S_FALSE（本就为空）
	if ret == 0 || ret == 1 {
		return nil
	}
	return fmt.Errorf("清空回收站失败，错误码 0x%X", ret)
}

// RecycleBinInfo 返回回收站占用字节数与项目数。
func RecycleBinInfo() (int64, int64) {
	var info shQueryRBInfo
	info.CbSize = uint32(unsafe.Sizeof(info))
	r, _, _ := procSHQueryRecycleBinW.Call(0, uintptr(unsafe.Pointer(&info)))
	if r != 0 {
		return 0, 0
	}
	return info.ISize, info.INumItems
}

// ── 磁盘 ──────────────────────────────────────────────────────────────────

type VolumeInfo struct {
	Root        string `json:"root"`
	Label       string `json:"label"`
	FileSystem  string `json:"fileSystem"`
	TotalBytes  uint64 `json:"totalBytes"`
	FreeBytes   uint64 `json:"freeBytes"`
	UsedBytes   uint64 `json:"usedBytes"`
	UsedPercent float64 `json:"usedPercent"`
}

// DiskSpace 查询卷容量。root 形如 "C:" 或 "C:\"。
func DiskSpace(root string) (VolumeInfo, error) {
	vi := VolumeInfo{Root: root}
	d := root
	if !strings.HasSuffix(d, `\`) {
		d += `\`
	}
	ptr, err := syscall.UTF16PtrFromString(d)
	if err != nil {
		return vi, err
	}
	var freeToCaller, total, totalFree uint64
	r, _, callErr := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&freeToCaller)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		return vi, fmt.Errorf("GetDiskFreeSpaceEx(%s) 失败: %v", root, callErr)
	}
	vi.TotalBytes = total
	vi.FreeBytes = totalFree
	if total >= totalFree {
		vi.UsedBytes = total - totalFree
	}
	if total > 0 {
		vi.UsedPercent = float64(vi.UsedBytes) / float64(total) * 100
	}

	var label [261]uint16
	var fsName [261]uint16
	var serial, maxComp, flags uint32
	lp, _ := syscall.UTF16PtrFromString(strings.TrimRight(d, `\`) + `\`)
	ok, _, _ := procGetVolumeInformationW.Call(
		uintptr(unsafe.Pointer(lp)),
		uintptr(unsafe.Pointer(&label[0])), 260,
		uintptr(unsafe.Pointer(&serial)),
		uintptr(unsafe.Pointer(&maxComp)),
		uintptr(unsafe.Pointer(&flags)),
		uintptr(unsafe.Pointer(&fsName[0])), 260,
	)
	if ok != 0 {
		vi.Label = syscall.UTF16ToString(label[:])
		vi.FileSystem = syscall.UTF16ToString(fsName[:])
	}
	return vi, nil
}

// ListVolumes 枚举当前可用的固定磁盘卷。
func ListVolumes() []VolumeInfo {
	var out []VolumeInfo
	var buf [512]uint16
	r, _, _ := procGetLogicalDriveStringsW.Call(512, uintptr(unsafe.Pointer(&buf[0])))
	if r == 0 {
		return out
	}
	s := syscall.UTF16ToString(buf[:])
	for _, part := range strings.Split(s, "\x00") {
		if part == "" {
			continue
		}
		rtype, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(mustUTF16(part))))
		if rtype != 3 { // DRIVE_FIXED
			continue
		}
		if vi, err := DiskSpace(part); err == nil {
			out = append(out, vi)
		}
	}
	return out
}

var (
	procGetLogicalDriveStringsW = modKernel32.NewProc("GetLogicalDriveStringsW")
	procGetDriveTypeW            = modKernel32.NewProc("GetDriveTypeW")
)

func mustUTF16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

// ── Shell 交互 ────────────────────────────────────────────────────────────

// ShowInExplorer 在资源管理器中定位文件。
func ShowInExplorer(path string) error {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	verb, _ := syscall.UTF16PtrFromString("open")
	file, _ := syscall.UTF16PtrFromString("explorer.exe")
	args, _ := syscall.UTF16PtrFromString(`/select,"` + path + `"`)
	r, _, _ := procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(args)),
		0, SW_SHOWNORMAL)
	if r <= 32 {
		_ = p
		return errors.New("无法打开资源管理器")
	}
	return nil
}

// OpenPath 用默认程序打开路径。
func OpenPath(path string) error {
	return OpenWithArgs(path)
}

// OpenWithArgs 以指定参数启动一个程序。
func OpenWithArgs(exe string, args ...string) error {
	verb, _ := syscall.UTF16PtrFromString("open")
	file, _ := syscall.UTF16PtrFromString(exe)
	var argsPtr uintptr
	if len(args) > 0 {
		a, _ := syscall.UTF16PtrFromString(strings.Join(args, " "))
		argsPtr = uintptr(unsafe.Pointer(a))
	}
	r, _, _ := procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		argsPtr, 0, SW_SHOWNORMAL)
	if r <= 32 {
		return errors.New("无法启动程序：" + exe)
	}
	return nil
}

// RunAsAdmin 以管理员身份重新启动当前程序。
func RunAsAdmin(extraArgs string) error {
	exe := ExePath()
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	var argsPtr uintptr
	if extraArgs != "" {
		a, _ := syscall.UTF16PtrFromString(extraArgs)
		argsPtr = uintptr(unsafe.Pointer(a))
	}
	r, _, _ := procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		argsPtr, 0, SW_SHOWNORMAL)
	if r <= 32 {
		return errors.New("提权被拒绝或已取消")
	}
	return nil
}

// MessageBox 弹出一个原生消息框。
func MessageBox(title, text string, flags uint32) {
	t, _ := syscall.UTF16PtrFromString(title)
	c, _ := syscall.UTF16PtrFromString(text)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(t)), uintptr(flags))
}

// MarkSelfDeleteOnReboot 让文件在下次重启时被系统删除（用于卸载自清理）。
func MarkSelfDeleteOnReboot(path string) error {
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	r, _, callErr := procMoveFileExW.Call(uintptr(unsafe.Pointer(ptr)), 0, MOVEFILE_DELAY_UNTIL_REBOOT)
	if r == 0 {
		return fmt.Errorf("MoveFileEx 延迟删除失败: %v", callErr)
	}
	return nil
}

// PathExists 判断路径是否存在。
func PathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// ── 窗口与外观 ────────────────────────────────────────────────────────────

var (
	procSetProcessDpiAwarenessContext = modUser32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = modUser32.NewProc("SetProcessDPIAware")
	procSendMessageW                  = modUser32.NewProc("SendMessageW")
	procLoadImageW                    = modUser32.NewProc("LoadImageW")
	procGetModuleHandleW              = modKernel32.NewProc("GetModuleHandleW")
	procCreateMutexW                  = modKernel32.NewProc("CreateMutexW")
	procReleaseMutex                  = modKernel32.NewProc("ReleaseMutex")
	modDwmapi                         = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute         = modDwmapi.NewProc("DwmSetWindowAttribute")
)

// EnableDPIAwareness 让界面在高分屏下保持清晰（老系统自动降级）。
func EnableDPIAwareness() {
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = (HANDLE)-4
	if err := procSetProcessDpiAwarenessContext.Find(); err == nil {
		_, _, _ = procSetProcessDpiAwarenessContext.Call(^uintptr(3))
		return
	}
	if err := procSetProcessDPIAware.Find(); err == nil {
		_, _, _ = procSetProcessDPIAware.Call()
	}
}

// SetDarkTitleBar 切换标题栏深色模式（Windows 10 1809+）。
func SetDarkTitleBar(hwnd uintptr, dark bool) {
	if hwnd == 0 {
		return
	}
	const attrDarkMode = 20
	const attrDarkModePre20 = 19
	v := uint32(0)
	if dark {
		v = 1
	}
	r, _, _ := procDwmSetWindowAttribute.Call(hwnd, attrDarkMode, uintptr(unsafe.Pointer(&v)), 4)
	if r != 0 {
		procDwmSetWindowAttribute.Call(hwnd, attrDarkModePre20, uintptr(unsafe.Pointer(&v)), 4)
	}
}

// ApplyWindowIcon 把可执行文件内资源 ID 1 的图标设为窗口图标。
func ApplyWindowIcon(hwnd uintptr, resID uintptr) {
	if hwnd == 0 {
		return
	}
	const (
		wmSetIcon   = 0x0080
		iconSmall   = 0
		iconBig     = 1
		imageIcon   = 1
		lrShared    = 0x00008000
		lrDefaultSz = 0x00000040
	)
	hInst, _, _ := procGetModuleHandleW.Call(0)
	for _, kind := range []uintptr{iconSmall, iconBig} {
		h, _, _ := procLoadImageW.Call(hInst, resID, imageIcon, 0, 0, lrShared|lrDefaultSz)
		if h != 0 {
			procSendMessageW.Call(hwnd, wmSetIcon, kind, h)
		}
	}
}

// SingleInstance 通过命名互斥体保证同一时刻只运行一个实例。
// 返回值 release 用于释放互斥体；first 表示本进程是否为第一个实例。
func SingleInstance(name string) (release func(), first bool) {
	ptr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return func() {}, true
	}
	h, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(ptr)))
	if h == 0 {
		return func() {}, true
	}
	if errnoOf(callErr) == syscall.ERROR_ALREADY_EXISTS {
		return func() {}, false
	}
	return func() { procReleaseMutex.Call(h) }, true
}

// KillProcessesByName 结束指定进程名的所有实例（用于卸载前收尾）。
func KillProcessesByName(name string) {
	cmd := execCommand("taskkill", "/F", "/IM", name)
	_ = cmd.Run()
}
