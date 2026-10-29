// Package fsx 提供支持长路径、跳过重解析点的高性能并行遍历。
package fsx

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetFileAttributesW = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetFileAttributesW")

// Entry 是一次遍历命中的条目。
type Entry struct {
	Path      string
	Size      int64
	Mod       time.Time
	IsDir     bool
	IsReparse bool
}

// LongPath 在需要时加上 \\?\ 前缀以突破 260 字符限制。
func LongPath(p string) string {
	if strings.HasPrefix(p, `\\?\`) {
		return p
	}
	p = filepath.Clean(p)
	if strings.HasPrefix(p, `\\`) {
		if len(p) >= 240 {
			return `\\?\UNC\` + p[2:]
		}
		return p
	}
	if len(p) >= 240 {
		return `\\?\` + p
	}
	return p
}

// Options 控制遍历行为。
type Options struct {
	Workers int

	// SkipDir 返回 true 时整棵子树被跳过（入参为普通路径）。
	SkipDir func(path string) bool

	// SkipFile 返回 true 时该文件被忽略。
	SkipFile func(path string) bool

	// OnFile 对每个文件调用。
	OnFile func(e Entry)

	// OnProgress 周期性回调，返回 false 可中止遍历。
	OnProgress func(visited int64, bytes int64) bool

	ProgressEvery int64
}

// Walk 并行遍历 root 下的全部文件。
// 自动跳过重解析点（junction / 符号链接）以避免循环与重复计数。
func Walk(ctx context.Context, root string, opt Options) (int64, int64) {
	workers := opt.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
		if workers > 8 {
			workers = 8
		}
		if workers < 2 {
			workers = 2
		}
	}
	every := opt.ProgressEvery
	if every <= 0 {
		every = 2000
	}

	var (
		mu       sync.Mutex
		items    []string
		active   int
		closed   bool
		visited  int64
		bytes    int64
		lastTick int64
		stop     bool
	)
	cond := sync.NewCond(&mu)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				for len(items) == 0 && active > 0 && !closed {
					cond.Wait()
				}
				if (len(items) == 0 && active == 0) || closed || stop {
					cond.Broadcast()
					mu.Unlock()
					return
				}
				dir := items[len(items)-1]
				items = items[:len(items)-1]
				active++
				mu.Unlock()

				entries, err := os.ReadDir(LongPath(dir))
				if err == nil {
					for _, de := range entries {
						if stop {
							break
						}
						name := de.Name()
						full := filepath.Join(dir, name)
						isReparse := de.Type()&fs.ModeSymlink != 0

						if de.IsDir() {
							if isReparse || isReparseAttr(LongPath(full)) {
								continue
							}
							if opt.SkipDir != nil && opt.SkipDir(full) {
								continue
							}
							mu.Lock()
							items = append(items, full)
							cond.Signal()
							mu.Unlock()
							continue
						}
						if !de.Type().IsRegular() {
							continue
						}
						if isReparseAttr(LongPath(full)) {
							continue
						}
						if opt.SkipFile != nil && opt.SkipFile(full) {
							continue
						}
						fi, err := de.Info()
						if err != nil {
							continue
						}
						e := Entry{Path: full, Size: fi.Size(), Mod: fi.ModTime()}
						if opt.OnFile != nil {
							opt.OnFile(e)
						}
						mu.Lock()
						visited++
						bytes += e.Size
						needTick := opt.OnProgress != nil && visited-lastTick >= every
						if needTick {
							lastTick = visited
						}
						mu.Unlock()
						if needTick && !opt.OnProgress(visited, bytes) {
							mu.Lock()
							stop = true
							cond.Broadcast()
							mu.Unlock()
							return
						}
						if stop {
							return
						}
					}
				}

				mu.Lock()
				active--
				if len(items) == 0 && active == 0 {
					cond.Broadcast()
				}
				mu.Unlock()
			}
		}()
	}

	mu.Lock()
	items = append(items, filepath.Clean(root))
	cond.Signal()
	mu.Unlock()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			mu.Lock()
			stop = true
			closed = true
			cond.Broadcast()
			mu.Unlock()
		case <-done:
		}
	}()

	wg.Wait()
	close(done)

	mu.Lock()
	defer mu.Unlock()
	return visited, bytes
}

func isReparseAttr(p string) bool {
	ptr, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return false
	}
	r, _, _ := procGetFileAttributesW.Call(uintptr(unsafe.Pointer(ptr)))
	const invalid = 0xFFFFFFFF
	if uint32(r) == invalid {
		return false
	}
	return r&0x400 != 0
}

// DirSize 快速统计目录体积（用于残留扫描）。
func DirSize(root string) (int64, int64) {
	return Walk(context.Background(), root, Options{})
}
