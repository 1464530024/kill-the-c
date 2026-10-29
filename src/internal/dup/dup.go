// Package dup 实现重复文件扫描：大小分组 → 快速指纹 → 全量 SHA-256 三级过滤。
package dup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/crc64"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"killthec/internal/fsx"
	"killthec/internal/store"
)

const (
	quickWindow = 64 << 10  // 首尾各取 64KB 做快速指纹
	quickWhole  = 128 << 10 // 小于此尺寸的文件直接全量哈希
	maxCand     = 800000
)

// systemDrive 返回系统盘前缀（形如 "c:"）。
func systemDrive() string {
	d := os.Getenv("SystemDrive")
	if d == "" {
		d = "C:"
	}
	return strings.ToLower(d)
}

// skipPrefixes：系统保护区，里面的文件不可清理，列出来也没有意义。
func skipProtected(p string) bool {
	lp := strings.ToLower(filepath.Clean(p))
	d := systemDrive()
	for _, s := range []string{
		d + `\windows`,
		d + `\program files`,
		d + `\program files (x86)`,
		d + `\$recycle.bin`,
		d + `\system volume information`,
		d + `ecovery`,
		d + `\perflogs`,
		d + `\msocache`,
		d + `\programdata\microsoft\windows`,
	} {
		if lp == s || strings.HasPrefix(lp, s+`\`) {
			return true
		}
	}
	return false
}

type File struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Mod  int64  `json:"mod"`
	Dir  string `json:"dir"`
	Keep bool   `json:"keep"`
}

type Group struct {
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	Count  int    `json:"count"`
	Wasted int64  `json:"wasted"`
	Files  []File `json:"files"`
}

type Result struct {
	Roots        []string `json:"roots"`
	Scanned      int64    `json:"scanned"`
	Candidates   int64    `json:"candidates"`
	GroupCount   int      `json:"groupCount"`
	DupFiles     int      `json:"dupFiles"`
	WastedBytes  int64    `json:"wastedBytes"`
	TotalDupSize int64    `json:"totalDupSize"`
	Groups       []Group  `json:"groups"`
	ElapsedMs    int64    `json:"elapsedMs"`
	Truncated    bool     `json:"truncated"`
}

type Progress struct {
	Phase   string `json:"phase"`
	Current string `json:"current"`
	Scanned int64  `json:"scanned"`
	Total   int64  `json:"total"`
	Groups  int    `json:"groups"`
	Elapsed int64  `json:"elapsed"`
}

type Options struct {
	Roots      []string
	MinSize    int64
	MaxGroups  int
	OnProgress func(Progress) bool
}

type cand struct {
	File
	whole   bool
	fullKey string
}

var variant = crc64.MakeTable(crc64.ECMA)

var skipFrags = []string{
	`\appdata\local\temp`,
	`\windows\`,
	`\program files\`,
	`\program files (x86)\`,
	`\$recycle.bin`,
	`\system volume information`,
	`\appdata\local\packages\`,
	`\appdata\local\microsoft\windows\inetcache`,
	`\appdata\local\microsoft\edge\`,
	`\appdata\local\google\chrome\`,
	`\node_modules\`,
	`\.git\`,
	`\appdata\local\temp\`,
}

func okPath(p string) bool {
	lp := strings.ToLower(filepath.Clean(p))
	for _, s := range skipFrags {
		if strings.Contains(lp, s) {
			return false
		}
	}
	return !store.IsWhitelisted(p)
}

// DriveRoot 返回整个系统盘根目录。
func DriveRoot() string {
	return systemDrive() + `\`
}

// PersonalRoots 返回用户个人目录（桌面/文档/下载/图片/视频/音乐）。
func PersonalRoots() []string {
	home, _ := os.UserHomeDir()
	var out []string
	for _, n := range []string{"Desktop", "Documents", "Downloads", "Pictures", "Videos", "Music"} {
		p := filepath.Join(home, n)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			out = append(out, p)
		}
	}
	if len(out) == 0 && home != "" {
		out = append(out, home)
	}
	return out
}

// DefaultRoots 默认扫描整个系统盘（系统保护区与白名单仍会被跳过）。
func DefaultRoots() []string {
	return []string{DriveRoot()}
}

func workers() int {
	n := runtime.NumCPU()
	if n < 4 {
		n = 4
	}
	if n > 8 {
		n = 8
	}
	return n
}

// Run 执行重复文件扫描。
func Run(ctx context.Context, opt Options) *Result {
	start := time.Now()
	res := &Result{Roots: opt.Roots}
	if opt.MinSize <= 0 {
		opt.MinSize = 1 << 20
	}
	if opt.MaxGroups <= 0 {
		opt.MaxGroups = 600
	}
	if len(opt.Roots) == 0 {
		opt.Roots = DefaultRoots()
		res.Roots = opt.Roots
	}
	emit := func(p Progress) {
		if opt.OnProgress != nil {
			opt.OnProgress(p)
		}
	}

	// ── 第一级：按大小分组 ────────────────────────────────────────────
	bySize := map[int64][]cand{}
	var mu sync.Mutex
	var scanned int64
	for _, root := range res.Roots {
		if ctx.Err() != nil {
			break
		}
		fsx.Walk(ctx, root, fsx.Options{
			Workers:       workers(),
			SkipDir:       func(p string) bool { return !okPath(p) },
			SkipFile:      func(p string) bool { return !okPath(p) },
			ProgressEvery: 6000,
			OnProgress: func(v, b int64) bool {
				emit(Progress{Phase: "scan", Scanned: v, Elapsed: time.Since(start).Milliseconds()})
				return ctx.Err() == nil
			},
			OnFile: func(e fsx.Entry) {
				if e.Size < opt.MinSize {
					return
				}
				mu.Lock()
				scanned++
				if len(bySize) < maxCand {
					bySize[e.Size] = append(bySize[e.Size], cand{File: File{
						Path: e.Path, Name: filepath.Base(e.Path), Size: e.Size,
						Mod: e.Mod.Unix(), Dir: filepath.Dir(e.Path),
					}})
				}
				mu.Unlock()
			},
		})
	}
	res.Scanned = scanned

	cands := make([]cand, 0, 1024)
	for _, v := range bySize {
		if len(v) > 1 {
			cands = append(cands, v...)
		}
	}
	res.Candidates = int64(len(cands))
	if len(cands) == 0 {
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res
	}

	// ── 第二级：快速指纹（≤128KB 直接全量）───────────────────────────
	type qkey struct {
		whole bool
		h     string
	}
	quickGroups := map[qkey][]cand{}
	var qmu sync.Mutex
	total := int64(len(cands))
	runPool(ctx, workers(), cands, func(c cand) {
		if c.Size <= quickWhole {
			sum, err := fullHash(c.Path)
			if err == nil {
				c.whole, c.fullKey = true, sum
				k := qkey{true, sum + "|" + strconv.FormatInt(c.Size, 10)}
				qmu.Lock()
				quickGroups[k] = append(quickGroups[k], c)
				qmu.Unlock()
			}
			return
		}
		h, err := partialHash(c.Path, c.Size)
		if err == nil {
			c.whole, c.fullKey = false, ""
			k := qkey{false, fmt.Sprintf("%016x|%d", h, c.Size)}
			qmu.Lock()
			quickGroups[k] = append(quickGroups[k], c)
			qmu.Unlock()
		}
	}, func(d int64) {
		emit(Progress{Phase: "hash1", Scanned: d, Total: total, Elapsed: time.Since(start).Milliseconds()})
	})

	// ── 第三级：全量 SHA-256（仅对第二级仍有冲突的大文件）────────────
	var needFull []cand
	for k, v := range quickGroups {
		if len(v) < 2 || k.whole {
			continue
		}
		needFull = append(needFull, v...)
	}

	fullGroups := map[string][]cand{}
	var fmu sync.Mutex
	// 已经全量哈希过的小文件组直接进入最终结果
	for k, v := range quickGroups {
		if len(v) < 2 || !k.whole {
			continue
		}
		key := k.h
		fullGroups[key] = append(fullGroups[key], v...)
	}

	if len(needFull) > 0 {
		total2 := int64(len(needFull))
		runPool(ctx, workers(), needFull, func(c cand) {
			sum, err := fullHash(c.Path)
			if err == nil {
				c.fullKey = sum
				key := sum + "|" + strconv.FormatInt(c.Size, 10)
				fmu.Lock()
				fullGroups[key] = append(fullGroups[key], c)
				fmu.Unlock()
			}
		}, func(d int64) {
			emit(Progress{Phase: "hash2", Scanned: d, Total: total2, Elapsed: time.Since(start).Milliseconds()})
		})
	}

	// ── 汇总 ─────────────────────────────────────────────────────────
	for k, v := range fullGroups {
		if len(v) < 2 {
			continue
		}
		var size int64
		if i := strings.LastIndex(k, "|"); i >= 0 {
			size, _ = strconv.ParseInt(k[i+1:], 10, 64)
		}
		g := Group{Key: k, Size: size, Count: len(v)}
		files := make([]File, 0, len(v))
		for _, c := range v {
			files = append(files, c.File)
		}
		sort.Slice(files, func(i, j int) bool {
			if files[i].Mod != files[j].Mod {
				return files[i].Mod < files[j].Mod
			}
			return files[i].Path < files[j].Path
		})
		files[0].Keep = true
		for i := 1; i < len(files); i++ {
			files[i].Keep = false
		}
		g.Files = files
		g.Wasted = size * int64(len(files)-1)
		res.Groups = append(res.Groups, g)
	}
	sort.Slice(res.Groups, func(i, j int) bool {
		if res.Groups[i].Wasted != res.Groups[j].Wasted {
			return res.Groups[i].Wasted > res.Groups[j].Wasted
		}
		return res.Groups[i].Size > res.Groups[j].Size
	})
	res.GroupCount = len(res.Groups)
	for _, g := range res.Groups {
		res.DupFiles += g.Count - 1
		res.WastedBytes += g.Wasted
		for _, f := range g.Files {
			res.TotalDupSize += f.Size
		}
	}
	if len(res.Groups) > opt.MaxGroups {
		res.Groups = res.Groups[:opt.MaxGroups]
		res.Truncated = true
	}
	res.ElapsedMs = time.Since(start).Milliseconds()
	emit(Progress{Phase: "done", Scanned: total, Total: total, Groups: res.GroupCount, Elapsed: res.ElapsedMs})
	return res
}

var runPoolPlaceholder int
// runPool 以固定并发度处理任务，并每 250ms 汇报一次进度。
func runPool(ctx context.Context, n int, items []cand, work func(cand), onTick func(int64)) {
	if len(items) == 0 {
		return
	}
	ch := make(chan cand, 256)
	var wg sync.WaitGroup
	var counter int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range ch {
				work(c)
				atomic.AddInt64(&counter, 1)
			}
		}()
	}
	go func() {
		defer close(ch)
		for _, c := range items {
			select {
			case <-ctx.Done():
				return
			case ch <- c:
			}
		}
	}()

	doneCh := make(chan struct{})
	tk := time.NewTicker(250 * time.Millisecond)
	go func() {
		for {
			select {
			case <-doneCh:
				return
			case <-tk.C:
				if onTick != nil {
					onTick(atomic.LoadInt64(&counter))
				}
			}
		}
	}()
	wg.Wait()
	close(doneCh)
	tk.Stop()
	if onTick != nil {
		onTick(atomic.LoadInt64(&counter))
	}
}

func partialHash(path string, size int64) (uint64, error) {
	f, err := os.Open(fsx.LongPath(path))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	h := crc64.New(variant)
	if _, err := io.CopyN(h, f, quickWindow); err != nil {
		return 0, err
	}
	if _, err := f.Seek(-quickWindow, io.SeekEnd); err != nil {
		return 0, err
	}
	if _, err := io.CopyN(h, f, quickWindow); err != nil {
		return 0, err
	}
	var szb [8]byte
	for i := 0; i < 8; i++ {
		szb[i] = byte(uint64(size) >> (8 * i))
	}
	h.Write(szb[:])
	return h.Sum64(), nil
}

func fullHash(path string) (string, error) {
	f, err := os.Open(fsx.LongPath(path))
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
