// 开发工具：从任意 exe 中提取内嵌图标为 BMP，用于核对图标资源。
//
//	go run ./tools/icondump <exe> <尺寸> <输出.bmp>
package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	pPrivateExtractIconsW = user32.NewProc("PrivateExtractIconsW")
	pDrawIconEx           = user32.NewProc("DrawIconEx")
	pGetDC                = user32.NewProc("GetDC")
	pReleaseDC            = user32.NewProc("ReleaseDC")
	pCreateCompatibleDC   = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection     = gdi32.NewProc("CreateDIBSection")
	pSelectObject         = gdi32.NewProc("SelectObject")
	pDeleteDC             = gdi32.NewProc("DeleteDC")
	pDeleteObject         = gdi32.NewProc("DeleteObject")
	pPatBlt               = gdi32.NewProc("PatBlt")
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

func main() {
	if len(os.Args) < 4 {
		fmt.Println("用法: icondump <exe> <尺寸> <输出.bmp>")
		os.Exit(2)
	}
	exe := os.Args[1]
	size, _ := strconv.Atoi(os.Args[2])
	dst := os.Args[3]

	exeW, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		panic(err)
	}
	var hIcon, iconID uintptr
	n, _, _ := pPrivateExtractIconsW.Call(
		uintptr(unsafe.Pointer(exeW)), 0,
		uintptr(size), uintptr(size),
		uintptr(unsafe.Pointer(&hIcon)), uintptr(unsafe.Pointer(&iconID)), 1, 0)
	fmt.Printf("PrivateExtractIcons(%s, %d) -> 数量=%d hIcon=%#x\n", exe, size, n, hIcon)
	if n == 0 || hIcon == 0 {
		fmt.Println("未取到图标")
		os.Exit(1)
	}

	hdcScreen, _, _ := pGetDC.Call(0)
	memDC, _, _ := pCreateCompatibleDC.Call(hdcScreen)
	bi := struct {
		Hdr   bitmapInfoHeader
		Color [3]uint32
	}{}
	bi.Hdr.Size = 40
	bi.Hdr.Width = int32(size)
	bi.Hdr.Height = -int32(size)
	bi.Hdr.Planes = 1
	bi.Hdr.BitCount = 32
	var pix uintptr
	bmp, _, _ := pCreateDIBSection.Call(hdcScreen, uintptr(unsafe.Pointer(&bi)),
		0, uintptr(unsafe.Pointer(&pix)), 0, 0)
	pSelectObject.Call(memDC, bmp)
	pPatBlt.Call(memDC, 0, 0, uintptr(size), uintptr(size), 0x00F00021) // 白底
	pDrawIconEx.Call(memDC, 0, 0, hIcon, 0, 0, 0, 0, 3)                 // DI_NORMAL

	px := unsafe.Slice((*byte)(unsafe.Pointer(pix)), size*size*4)
	row := size * 4
	out := make([]byte, 0, 54+row*size)
	out = append(out, 'B', 'M')
	put := func(v uint32, n int) {
		for i := 0; i < n; i++ {
			out = append(out, byte(v>>(8*i)))
		}
	}
	put(uint32(54+row*size), 4)
	put(0, 4)
	put(54, 4)
	hdr := bitmapInfoHeader{Size: 40, Width: int32(size), Height: int32(size),
		Planes: 1, BitCount: 32, SizeImage: uint32(row * size)}
	hb := (*[40]byte)(unsafe.Pointer(&hdr))
	out = append(out, hb[:]...)
	for y := size - 1; y >= 0; y-- {
		out = append(out, px[y*row:(y+1)*row]...)
	}
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		panic(err)
	}
	fmt.Println("已写出", dst)

	pDeleteObject.Call(bmp)
	pDeleteDC.Call(memDC)
	pReleaseDC.Call(0, hdcScreen)
}
