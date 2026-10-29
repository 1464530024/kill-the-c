// 开发工具：用资源管理器同款的 ExtractIconEx 验证 exe 图标能否被取出。
//
//	go run ./tools/icontest <exe>
package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	shell32      = syscall.NewLazyDLL("shell32.dll")
	pExtractEx   = shell32.NewProc("ExtractIconExW")
	pDestroyIcon = syscall.NewLazyDLL("user32.dll").NewProc("DestroyIcon")
)

func main() {
	exe := os.Args[1]
	p, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		panic(err)
	}
	var large, small uintptr
	n, _, _ := pExtractEx.Call(
		uintptr(unsafe.Pointer(p)), 0,
		uintptr(unsafe.Pointer(&large)), uintptr(unsafe.Pointer(&small)), 1)
	fmt.Printf("ExtractIconEx(%s) -> 提取数量=%d  large=%#x  small=%#x\n", exe, n, large, small)
	if n > 0 && large != 0 {
		fmt.Println("  ✅ 资源管理器可以取到图标句柄")
		pDestroyIcon.Call(large)
	} else {
		fmt.Println("  ❌ 取不到图标（快捷方式会显示白页占位图）")
	}
	if small != 0 {
		pDestroyIcon.Call(small)
	}
}
