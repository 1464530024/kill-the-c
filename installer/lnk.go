package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

// 直接按 Shell Link 二进制规范（MS-SHLLINK）生成 .lnk，避免依赖 COM 实现的虚表细节。

var (
	procWideCharToMultiByte = syscall.NewLazyDLL("kernel32.dll").NewProc("WideCharToMultiByte")
	pILCreateFromPathW      = shell32.NewProc("ILCreateFromPathW")
	pILGetSize              = shell32.NewProc("ILGetSize")
	pILFree                 = shell32.NewProc("ILFree")
	pSHChangeNotify         = shell32.NewProc("SHChangeNotify")
)

const (
	shcneCreate       = 0x00000002
	shcneUpdateItem   = 0x00002000
	shcneAssocChanged = 0x08000000
	shcnfIDList       = 0x0000
	shcnfPathW        = 0x0005
	shcnfFlush        = 0x1000
)

// notifyShellItem 通知资源管理器某个文件已新建/更新并立即刷新，避免图标被缓存成旧结果。
func notifyShellItem(path string) {
	if path == "" {
		return
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	_, _, _ = pSHChangeNotify.Call(
		uintptr(shcneCreate|shcneUpdateItem),
		uintptr(shcnfPathW|shcnfFlush),
		uintptr(unsafe.Pointer(p)), 0)
}

// notifyShellRefresh 让资源管理器重建受影响的图标/关联缓存。
func notifyShellRefresh() {
	_, _, _ = pSHChangeNotify.Call(
		uintptr(shcneAssocChanged),
		uintptr(shcnfIDList|shcnfFlush), 0, 0)
}

// targetIDList 通过 ILCreateFromPathW 取得与资源管理器一致的目标 ID 列表。
func targetIDList(path string) []byte {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil
	}
	pidl, _, _ := pILCreateFromPathW.Call(uintptr(unsafe.Pointer(p)))
	if pidl == 0 {
		return nil
	}
	defer pILFree.Call(pidl)
	size, _, _ := pILGetSize.Call(pidl)
	if size == 0 || size > 0xFFF0 {
		return nil
	}
	src := unsafe.Slice((*byte)(unsafe.Pointer(pidl)), int(size))
	out := make([]byte, 2+int(size))
	binary.LittleEndian.PutUint16(out[:2], uint16(size))
	copy(out[2:], src)
	return out
}

func ansiBytes(s string) []byte {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return []byte(s)
	}
	const cpACP = 0
	n, _, _ := procWideCharToMultiByte.Call(cpACP, 0,
		uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), 0, 0, 0, 0)
	if n == 0 {
		return []byte(s)
	}
	buf := make([]byte, n)
	procWideCharToMultiByte.Call(cpACP, 0,
		uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1),
		uintptr(unsafe.Pointer(&buf[0])), n, 0, 0)
	return buf
}

func putUTF16String(buf *[]byte, s string) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		u = []uint16{0}
	}
	chars := len(u) - 1
	var tmp [2]byte
	binary.LittleEndian.PutUint16(tmp[:], uint16(chars))
	*buf = append(*buf, tmp[:]...)
	for i := 0; i < chars; i++ {
		binary.LittleEndian.PutUint16(tmp[:], u[i])
		*buf = append(*buf, tmp[:]...)
	}
}

// createShortcut 生成指向 exePath 的快捷方式，写盘并通知 shell 刷新图标。
func createShortcut(lnk, exePath, workDir string) error {
	if lnk == "" {
		return fmt.Errorf("快捷方式路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(lnk), 0o755); err != nil {
		return err
	}

	const (
		flagHasIDList       = 0x00000001
		flagHasLinkInfo     = 0x00000002
		flagHasName         = 0x00000004
		flagHasRelativePath = 0x00000008
		flagHasWorkingDir   = 0x00000010
		flagHasIconLocation = 0x00000040
		flagIsUnicode       = 0x00000080
	)

	// 相对路径：以 .lnk 所在目录为基准（跨盘时 filepath.Rel 会失败，此时不写）
	relPath := ""
	if rd, err := filepath.Rel(filepath.Dir(lnk), exePath); err == nil &&
		filepath.VolumeName(rd) == "" && len(rd) < 240 {
		relPath = rd
	}
	flags := uint32(flagHasLinkInfo | flagHasName | flagHasWorkingDir | flagHasIconLocation | flagIsUnicode)
	if relPath != "" {
		flags |= flagHasRelativePath
	}
	idList := targetIDList(exePath)
	if len(idList) > 0 {
		flags |= flagHasIDList
	}

	now := uint64(time.Now().UnixNano()/100) + 116444736000000000
	out := make([]byte, 0, 1024)

	le := binary.LittleEndian
	var u32 [4]byte
	var u64b [8]byte
	w32 := func(v uint32) { le.PutUint32(u32[:], v); out = append(out, u32[:]...) }
	w64 := func(v uint64) { le.PutUint64(u64b[:], v); out = append(out, u64b[:]...) }

	// ── ShellLinkHeader ──
	w32(0x0000004C)
	out = append(out, []byte{0x01, 0x14, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}...)
	w32(flags)
	w32(0x00000020) // FILE_ATTRIBUTE_ARCHIVE
	w64(now)
	w64(now)
	w64(now)
	w32(0)                  // FileSize
	w32(0)                  // IconIndex
	w32(1)                  // ShowCommand = SW_SHOWNORMAL
	out = append(out, 0, 0) // HotKey
	out = append(out, 0, 0) // Reserved1
	w32(0)                  // Reserved2
	w32(0)                  // Reserved3

	// ── LinkTargetIDList ──
	if len(idList) > 0 {
		out = append(out, idList...)
	}

	// ── LinkInfo ──
	li := make([]byte, 0, 128)
	pathANSI := append(ansiBytes(exePath), 0)
	volID := make([]byte, 0, 24)
	volID = append(volID, 0, 0, 0, 0)    // VolumeIDSize（回填）
	volID = append(volID, 3, 0, 0, 0)    // DriveType = DRIVE_FIXED
	volID = append(volID, 0, 0, 0, 0)    // DriveSerialNumber
	volID = append(volID, 0x10, 0, 0, 0) // VolumeLabelOffset
	volID = append(volID, 0)             // 卷标：空串
	le.PutUint32(volID[0:4], uint32(len(volID)))

	const liHeader = 0x1C
	volOff := uint32(liHeader)
	pathOff := volOff + uint32(len(volID))
	suffixOff := pathOff + uint32(len(pathANSI))

	li = append(li, 0, 0, 0, 0) // LinkInfoSize
	li = append(li, 0, 0, 0, 0) // LinkInfoHeaderSize
	li = append(li, 0, 0, 0, 0) // LinkInfoFlags
	li = append(li, 0, 0, 0, 0) // VolumeIDOffset
	li = append(li, 0, 0, 0, 0) // LocalBasePathOffset
	li = append(li, 0, 0, 0, 0) // CommonNetworkRelativeLinkOffset
	li = append(li, 0, 0, 0, 0) // CommonPathSuffixOffset
	li = append(li, volID...)
	li = append(li, pathANSI...)
	li = append(li, 0)
	for len(li)%4 != 0 {
		li = append(li, 0)
	}
	le.PutUint32(li[0:4], uint32(len(li)))
	le.PutUint32(li[4:8], liHeader)
	le.PutUint32(li[8:12], 1)
	le.PutUint32(li[12:16], volOff)
	le.PutUint32(li[16:20], pathOff)
	le.PutUint32(li[20:24], 0)
	le.PutUint32(li[24:28], suffixOff)
	out = append(out, li...)

	// ── StringData ──
	putUTF16String(&out, appDisplayName+" 磁盘清理专家") // NAME_STRING
	if relPath != "" {
		putUTF16String(&out, relPath) // RELATIVE_PATH
	}
	putUTF16String(&out, workDir) // WORKING_DIR
	// 图标写纯路径（与系统自带快捷方式一致，不带 ",0"，兼容性更好）
	putUTF16String(&out, exePath)

	// ── ExtraData: TerminalBlock ──
	out = append(out, 0, 0, 0, 0)

	// 先写临时文件再原子改名，并 fsync，保证资源管理器读到的永远是完整文件
	tmp := lnk + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_ = os.Remove(lnk)
	if err := os.Rename(tmp, lnk); err != nil {
		return err
	}
	notifyShellItem(lnk)
	return nil
}
