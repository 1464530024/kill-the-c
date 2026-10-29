package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

const (
	appDisplayName = "Kill the C"
	appVersion     = "1.0.0"
	appExeName     = "KillTheC.exe"
	uninstallKey   = `Software\Microsoft\Windows\CurrentVersion\Uninstall\KillTheC`
)

// DefaultInstallDir 返回默认安装目录（用户级，无需管理员权限）。
func DefaultInstallDir() string {
	la := os.Getenv("LOCALAPPDATA")
	if la == "" {
		home, _ := os.UserHomeDir()
		la = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(la, "Programs", "KillTheC")
}

type progressFn func(pct int, status string)

// Install 执行完整安装流程。
func Install(dir string, desktopShortcut bool, files []byte, report progressFn) error {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	dir = filepath.Clean(filepath.FromSlash(dir))
	report(4, "正在准备安装目录…")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("无法创建安装目录：%w", err)
	}
	exePath := filepath.Join(dir, appExeName)

	report(10, "正在释放程序文件…")
	if err := writePayload(files, exePath, func(p int) {
		report(10+p*68/100, "正在释放程序文件…  "+fmt.Sprintf("%d%%", p))
	}); err != nil {
		return fmt.Errorf("写入程序文件失败：%w", err)
	}

	report(82, "正在创建快捷方式…")
	// 稍等一下，避免杀软扫描刚写入的 exe 时导致图标读取失败
	time.Sleep(150 * time.Millisecond)
	if err := createShortcut(startMenuPath(), exePath, dir); err != nil {
		// 快捷方式失败不阻断安装
		report(84, "开始菜单快捷方式创建失败，已跳过")
		time.Sleep(300 * time.Millisecond)
	}
	// 静默安装不主动新建桌面快捷方式，但若已存在则一并刷新
	// （覆盖升级时可以把目标路径与图标一起修正）
	if desktopShortcut || fileExists(desktopPath()) {
		_ = createShortcut(desktopPath(), exePath, dir)
	}

	report(90, "正在写入卸载信息…")
	if err := writeUninstallEntry(dir, exePath); err != nil {
		return fmt.Errorf("写入注册表失败：%w", err)
	}

	report(97, "正在完成安装…")
	_ = registerAppPaths(exePath)
	// 刷新资源管理器图标/关联缓存，确保快捷方式立刻显示正确图标
	notifyShellRefresh()
	report(100, "安装完成")
	return nil
}

func writePayload(gz []byte, dst string, report func(int)) error {
	zr, err := gzip.NewReader(strings.NewReader(string(gz)))
	if err != nil {
		return err
	}
	defer zr.Close()
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	var written int64
	total := int64(len(gz)) * 3
	buf := make([]byte, 256<<10)
	for {
		n, err := zr.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			if total > 0 {
				p := int(written * 100 / total)
				if p > 100 {
					p = 100
				}
				report(p)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func startMenuPath() string {
	ad := os.Getenv("APPDATA")
	if ad == "" {
		return ""
	}
	return filepath.Join(ad, "Microsoft", "Windows", "Start Menu", "Programs", appDisplayName+".lnk")
}

func desktopPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Desktop", appDisplayName+".lnk")
	}
	return ""
}

func writeUninstallEntry(dir, exePath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.WRITE)
	if err != nil {
		return err
	}
	defer k.Close()
	set := func(name, val string) { _ = k.SetStringValue(name, val) }
	set("DisplayName", appDisplayName+" 磁盘清理专家")
	set("DisplayVersion", appVersion)
	set("Publisher", "Kill the C Project")
	set("DisplayIcon", exePath+",0")
	set("InstallLocation", dir)
	cmd := `"` + exePath + `" --uninstall`
	set("UninstallString", cmd)
	set("QuietUninstallString", cmd)
	set("InstallDate", time.Now().Format("20060102"))
	set("URLInfoAbout", "")
	_ = k.SetDWordValue("NoModify", 1)
	_ = k.SetDWordValue("NoRepair", 1)
	if st, err := os.Stat(exePath); err == nil {
		_ = k.SetDWordValue("EstimatedSize", uint32(st.Size()/1024+256))
	}
	return nil
}

// registerAppPaths 在「应用和功能」中登记，并让 exe 出现在系统的程序列表中。
func registerAppPaths(exePath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\App Paths\`+appExeName, registry.WRITE)
	if err != nil {
		return err
	}
	defer k.Close()
	_ = k.SetStringValue("", exePath)
	_ = k.SetStringValue("Path", filepath.Dir(exePath))
	return nil
}

// ── 其它小工具 ────────────────────────────────────────────────────────────

var (
	launchAfter atomic.Bool
)

// pickFolder 弹出系统文件夹选择框。
func pickFolder(owner uintptr, initial string) string {
	bi := browseInfo{
		HwndOwner: owner,
		LpszTitle: u16("请选择 Kill the C 的安装位置"),
		UlFlags:   0x00000040 | 0x00000010, // BIF_NEWDIALOGSTYLE | BIF_EDITBOX
	}
	if initial != "" {
		bi.LpszTitle = u16("请选择 Kill the C 的安装位置（当前：" + initial + "）")
	}
	pidl, _, _ := pSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return ""
	}
	buf := make([]uint16, 1024)
	ok, _, _ := pSHGetPathFromIDListW.Call(pidl, uintptr(unsafe.Pointer(&buf[0])))
	if ok == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func messageBox(owner uintptr, title, text string, flags uint32) int {
	r, _, _ := pMessageBoxW.Call(owner, ui16(text), ui16(title), uintptr(flags))
	return int(r)
}

func launchApp(exe string) {
	verb := u16("open")
	r, _, _ := pShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(verb)), ui16(exe), 0, 0, SW_SHOWNORMAL)
	_ = r
}
