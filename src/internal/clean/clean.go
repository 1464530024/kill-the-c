// Package clean 执行清理动作，提供逐文件实时进度与安全兜底校验。
package clean

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"killthec/internal/classify"
	"killthec/internal/fsx"
	"killthec/internal/jobs"
	"killthec/internal/store"
	"killthec/internal/winapi"
)

// Item 是一个待清理目标。
type Item struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Result 是清理结果。
type Result struct {
	Deleted    int      `json:"deleted"`
	Failed     int      `json:"failed"`
	Skipped    int      `json:"skipped"`
	FreedBytes int64    `json:"freedBytes"`
	Method     string   `json:"method"`
	ElapsedMs  int64    `json:"elapsedMs"`
	Errors     []string `json:"errors"`
	Canceled   bool     `json:"canceled"`
}

const maxErrorsKept = 120

// Run 逐个删除目标。method 取 recycle（回收站）或 permanent（永久删除）。
func Run(ctx context.Context, job *jobs.Job, items []Item, method string) *Result {
	start := time.Now()
	res := &Result{Method: method}
	if method != "permanent" {
		method = "recycle"
		res.Method = "recycle"
	}

	// 深路径优先（目录在文件之后删除）
	sort.SliceStable(items, func(i, j int) bool {
		return len(items[i].Path) > len(items[j].Path)
	})

	total := len(items)
	var totalBytes int64
	for _, it := range items {
		totalBytes += it.Size
	}
	var freed int64
	var done int64

	job.Progress(jobs.Event{
		Phase: method, Current: "", Done: 0, Total: int64(total),
		TotalBytes: totalBytes, Message: "开始清理",
	})

	for _, it := range items {
		if ctx.Err() != nil {
			res.Canceled = true
			break
		}
		p := filepath.Clean(it.Path)
		if p == "" || p == "." {
			continue
		}
		// 安全兜底：白名单与受保护路径一律跳过
		if store.IsWhitelisted(p) {
			res.Skipped++
			continue
		}
		if isProtected(p) {
			res.Skipped++
			addErr(res, "已跳过受保护路径："+p)
			continue
		}

		job.Progress(jobs.Event{
			Phase: method, Current: p, Done: done, Total: int64(total),
			Bytes: freed, TotalBytes: totalBytes, Message: "正在删除",
		})

		var err error
		if method == "permanent" {
			_, err = winapi.DeletePermanent(p)
		} else {
			_, _, err = winapi.ToRecycleBin([]string{p})
		}
		if err != nil {
			res.Failed++
			addErr(res, p+"："+err.Error())
		} else {
			res.Deleted++
			freed += it.Size
		}
		done++
	}

	res.FreedBytes = freed
	res.ElapsedMs = time.Since(start).Milliseconds()
	job.Progress(jobs.Event{
		Phase: "done", Done: done, Total: int64(total),
		Bytes: freed, TotalBytes: totalBytes, Message: "清理完成",
	})
	return res
}

// EmptyRecycleBin 清空回收站。
func EmptyRecycleBin() (int64, error) {
	size, _ := winapi.RecycleBinInfo()
	if err := winapi.EmptyRecycleBin(); err != nil {
		return 0, err
	}
	return size, nil
}

func isProtected(p string) bool {
	lp := strings.ToLower(filepath.Clean(p))
	drive := strings.ToLower(filepath.VolumeName(p) + `\`)
	// 盘根目录绝对不允许
	if lp == drive || lp == drive[:len(drive)-1] {
		return true
	}
	// 关键系统路径
	for _, s := range []string{
		`c:\windows`, `c:\program files`, `c:\program files (x86)`,
		`c:\users\default`, `c:\users\public`, `c:\system volume information`,
		`c:\$recycle.bin`, `c:\perflogs`, `c:\recovery`, `c:\boot`,
	} {
		if lp == s {
			return true
		}
	}
	// 关键系统文件的直属父目录（C:\ 根）
	if strings.Count(lp, `\`) == 1 {
		base := strings.ToLower(filepath.Base(lp))
		if base == "windows" || base == "program files" || base == "program files (x86)" ||
			base == "users" || base == "programdata" || base == "perflogs" {
			return true
		}
	}
	// 单个文件：位于系统保护区
	for _, s := range []string{
		`c:\windows\system32`, `c:\windows\syswow64`, `c:\windows\winsxs`,
		`c:\windows\servicing`, `c:\windows\boot`, `c:\windows\fonts`,
		`c:\windows\assembly`, `c:\windows\microsoft.net`,
	} {
		if lp == s || strings.HasPrefix(lp, s+`\`) {
			return true
		}
	}
	return false
}

func addErr(res *Result, msg string) {
	if len(res.Errors) < maxErrorsKept {
		res.Errors = append(res.Errors, msg)
	}
}

// AssessPaths 批量返回路径的风险评估（供前端二次确认展示）。
func AssessPaths(paths []string) []classify.Assessment {
	out := make([]classify.Assessment, 0, len(paths))
	for _, p := range paths {
		out = append(out, classify.Assess(p, 0, time.Now()))
	}
	return out
}

// CategoryRequest 描述一次"按类目整类清理"。
type CategoryRequest struct {
	Root     string
	Cats     map[string]bool
	Deep     bool
	Method   string
	MinSize  int64 // 深度模式下纳入清理的最小体积
	KnownTotal int64
}

// RunByCategory 重新遍历磁盘，把指定类目的文件全部删除。
// 与列表选择不同，这会覆盖扫描阶段因数量上限而未展示的文件。
func RunByCategory(ctx context.Context, job *jobs.Job, req CategoryRequest) *Result {
	start := time.Now()
	res := &Result{Method: req.Method}
	if res.Method != "permanent" {
		res.Method = "recycle"
	}
	root := req.Root
	if root == "" {
		root = os.Getenv("SystemDrive") + `\`
	}
	minSize := req.MinSize
	if minSize <= 0 {
		minSize = 32 << 20
	}

	var done, freed, failed, skipped int64
	total := req.KnownTotal
	if total <= 0 {
		total = -1
	}
	pending := make([]string, 0, 64)
	const batch = 24

	flush := func() {
		if len(pending) == 0 {
			return
		}
		if res.Method == "permanent" {
			for _, p := range pending {
				if _, err := winapi.DeletePermanent(p); err != nil {
					failed++
					addErr(res, p+"："+err.Error())
				} else {
					done++
				}
			}
		} else {
			n, bad, _ := winapi.ToRecycleBin(pending)
			done += int64(n)
			failed += int64(len(bad))
			for _, b := range bad {
				addErr(res, "无法送入回收站："+b)
			}
		}
		pending = pending[:0]
	}

	fsx.Walk(ctx, root, fsx.Options{
		Workers:       4,
		ProgressEvery: 40,
		SkipDir:       func(p string) bool { return shouldSkip(p) },
		OnProgress: func(v, b int64) bool {
			flush()
			job.Progress(jobs.Event{
				Phase: res.Method, Done: done, Total: total,
				Bytes: freed, Message: "正在清理",
			})
			return ctx.Err() == nil
		},
		OnFile: func(e fsx.Entry) {
			if ctx.Err() != nil {
				return
			}
			a := classify.Assess(e.Path, e.Size, e.Mod)
			ok := false
			if req.Cats[a.Category] {
				if a.Junk {
					ok = true
				} else if req.Deep && a.Selectable && e.Size >= minSize {
					ok = true
				}
			}
			if !ok {
				return
			}
			if !a.Selectable || a.Risk == classify.RiskProtected {
				skipped++
				return
			}
			if isProtected(e.Path) || store.IsWhitelisted(e.Path) {
				skipped++
				return
			}
			freed += e.Size
			pending = append(pending, e.Path)
			job.Progress(jobs.Event{
				Phase: res.Method, Current: e.Path, Done: done, Total: total,
				Bytes: freed, Message: "正在清理",
			})
			if len(pending) >= batch {
				flush()
			}
		},
	})
	flush()

	res.Deleted = int(done)
	res.Failed = int(failed)
	res.Skipped = int(skipped)
	res.FreedBytes = freed
	res.Canceled = ctx.Err() != nil
	res.ElapsedMs = time.Since(start).Milliseconds()
	job.Progress(jobs.Event{
		Phase: "done", Done: done, Total: total,
		Bytes: freed, Message: "清理完成",
	})
	return res
}

// shouldSkip 与扫描阶段保持一致：跳过系统保护目录与白名单。
func shouldSkip(p string) bool {
	if store.IsWhitelisted(p) {
		return true
	}
	lp := strings.ToLower(filepath.Clean(p))
	for _, s := range []string{
		`c:\windows\winsxs`, `c:\$recycle.bin`, `c:\system volume information`,
		`c:\windows\servicing\lcus`, `c:\windows\containers`,
	} {
		if strings.HasPrefix(lp, s) {
			return true
		}
	}
	return false
}
