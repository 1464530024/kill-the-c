// Package scan 实现全盘扫描、分类统计与优先级 / 风险评估。
package scan

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"killthec/internal/classify"
	"killthec/internal/fsx"
	"killthec/internal/store"
	"killthec/internal/winapi"
)

const (
	maxStoredJunk   = 40000
	perCatJunk      = 1200
	perCatTop       = 30
	perCatSample    = 24
	maxBigFiles     = 200
	bigFileThreshold = 100 << 20
)

// FileItem 是返回给前端的一条文件记录。
type FileItem struct {
	Path       string   `json:"path"`
	Name       string   `json:"name"`
	Size       int64    `json:"size"`
	Mod        int64    `json:"mod"`
	Category   string   `json:"category"`
	Risk       string   `json:"risk"`
	Priority   int      `json:"priority"`
	Reasons    []string `json:"reasons"`
	Selectable bool     `json:"selectable"`
	Depth      string   `json:"depth"` // light | deep
}

// CatStat 是一个类别的统计结果。
type CatStat struct {
	Key       string     `json:"key"`
	Name      string     `json:"name"`
	Desc      string     `json:"desc"`
	Risk      string     `json:"risk"`
	Priority  int        `json:"priority"`
	Count     int64      `json:"count"`
	Bytes     int64      `json:"bytes"`
	JunkCount int64      `json:"junkCount"`
	JunkBytes int64      `json:"junkBytes"`
	TopFiles  []FileItem `json:"topFiles"`
	JunkFiles []FileItem `json:"junkFiles"`
	Truncated bool       `json:"truncated"`
	Synthetic bool       `json:"synthetic"`
}

// Result 是一次完整扫描的产物。
type Result struct {
	Root         string               `json:"root"`
	Volume       winapi.VolumeInfo    `json:"volume"`
	TotalFiles   int64                `json:"totalFiles"`
	TotalBytes   int64                `json:"totalBytes"`
	JunkCount    int64                `json:"junkCount"`
	JunkBytes    int64                `json:"junkBytes"`
	ReclaimBytes int64                `json:"reclaimBytes"`
	Categories   []CatStat            `json:"categories"`
	Light        []FileItem           `json:"light"`
	Deep         []FileItem           `json:"deep"`
	BigFiles     []FileItem           `json:"bigFiles"`
	Samples      map[string][]FileItem `json:"samples"`
	ElapsedMs    int64                `json:"elapsedMs"`
	Protected    []string             `json:"protected"`
	Errors       []string             `json:"errors"`
}

// Progress 是扫描进度。
type Progress struct {
	Phase   string `json:"phase"`
	Current string `json:"current"`
	Files   int64  `json:"files"`
	Bytes   int64  `json:"bytes"`
	Elapsed int64  `json:"elapsed"`
}

// Options 控制扫描范围。
type Options struct {
	Root       string
	Deep       bool
	SkipSystem bool
	OnProgress func(Progress) bool
}

var hardSkip = []string{
	`c:\windows\winsxs`,
	`c:\$recycle.bin`,
	`c:\system volume information`,
	`c:\windows\servicing\lcus`,
	`c:\windows\containers`,
}

func shouldSkipDir(p string, root string) bool {
	lp := strings.ToLower(filepath.Clean(p))
	if lp == strings.ToLower(filepath.Clean(root)) {
		return false
	}
	for _, s := range hardSkip {
		if strings.HasPrefix(lp, s) {
			return true
		}
	}
	if store.IsWhitelisted(p) {
		return true
	}
	return false
}

type catAcc struct {
	count int64
	bytes int64
	jc    int64
	jb    int64
	top   []FileItem
	junk  []FileItem
	sampl []FileItem
	trunc bool
}

// Run 执行一次完整扫描。
func Run(ctx context.Context, opt Options) *Result {
	start := time.Now()
	root := opt.Root
	if root == "" {
		root = "C:\\"
	}
	res := &Result{
		Root:    root,
		Samples: map[string][]FileItem{},
	}
	if vol, err := winapi.DiskSpace(root); err == nil {
		res.Volume = vol
	}
	res.Protected = append(res.Protected, "C:\\Windows\\WinSxS（组件存储，由系统自身维护）")
	rbSize, rbCount := winapi.RecycleBinInfo()
	_ = rbCount

	accs := map[string]*catAcc{}
	var amu sync.Mutex
	var cur atomic.Value
	cur.Store("")

	junkTotal := 0
	getAcc := func(k string) *catAcc {
		a, ok := accs[k]
		if !ok {
			a = &catAcc{}
			accs[k] = a
		}
		return a
	}

	onFile := func(e fsx.Entry) {
		cur.Store(filepath.Base(e.Path))
		a := classify.Assess(e.Path, e.Size, e.Mod)
		amu.Lock()
		defer amu.Unlock()
		ca := getAcc(a.Category)
		ca.count++
		ca.bytes += e.Size

		if a.Junk {
			ca.jc++
			ca.jb += e.Size
			if len(ca.junk) < perCatJunk && junkTotal < maxStoredJunk {
				ca.junk = append(ca.junk, mkItem(e, a, "light"))
				junkTotal++
			} else {
				ca.trunc = true
			}
		} else {
			if len(ca.sampl) < perCatSample {
				ca.sampl = append(ca.sampl, mkItem(e, a, "deep"))
			}
			// 深度模式候选：体积较大且可勾选
			if a.Selectable && e.Size >= 32<<20 {
				if len(ca.junk) < perCatJunk {
					ca.junk = append(ca.junk, mkItem(e, a, "deep"))
				} else {
					ca.trunc = true
				}
			}
		}

		if len(ca.top) < perCatTop {
			ca.top = append(ca.top, mkItem(e, a, "deep"))
		} else if ca.top[len(ca.top)-1].Size < e.Size {
			ca.top[len(ca.top)-1] = mkItem(e, a, "deep")
		}
		if len(ca.top) > 1 {
			sort.Slice(ca.top, func(i, j int) bool { return ca.top[i].Size > ca.top[j].Size })
		}
	}

	progressEvery := int64(1500)
	visited, bytes := fsx.Walk(ctx, root, fsx.Options{
		OnFile:        onFile,
		SkipDir:       func(p string) bool { return shouldSkipDir(p, root) },
		ProgressEvery: progressEvery,
		OnProgress: func(v, b int64) bool {
			if opt.OnProgress == nil {
				return true
			}
			c, _ := cur.Load().(string)
			return opt.OnProgress(Progress{
				Phase:   "scan",
				Current: c,
				Files:   v,
				Bytes:   b,
				Elapsed: time.Since(start).Milliseconds(),
			})
		},
	})

	res.TotalFiles = visited
	res.TotalBytes = bytes

	// 汇总类别
	for _, def := range classify.Categories {
		ca := accs[def.Key]
		if ca == nil || (ca.count == 0 && ca.jc == 0) {
			continue
		}
		st := CatStat{
			Key:       def.Key,
			Name:      def.Name,
			Desc:      def.Desc,
			Risk:      def.Risk,
			Priority:  def.Priority,
			Count:     ca.count,
			Bytes:     ca.bytes,
			JunkCount: ca.jc,
			JunkBytes: ca.jb,
			TopFiles:  ca.top,
			JunkFiles: ca.junk,
			Truncated: ca.trunc,
		}
		if len(ca.sampl) > 0 {
			res.Samples[def.Key] = ca.sampl
		}
		res.Categories = append(res.Categories, st)
		res.JunkCount += ca.jc
		res.JunkBytes += ca.jb
	}

	// 回收站伪类别
	if rbSize > 0 {
		res.Categories = append(res.Categories, CatStat{
			Key: "recycle", Name: "回收站", Desc: "已被删除的文件，选中后执行「清空回收站」释放空间",
			Risk: classify.RiskSafe, Priority: classify.PriCritical,
			JunkBytes: rbSize, Synthetic: true,
		})
		res.JunkBytes += rbSize
	}
	sort.Slice(res.Categories, func(i, j int) bool {
		if res.Categories[i].Priority != res.Categories[j].Priority {
			return res.Categories[i].Priority < res.Categories[j].Priority
		}
		return res.Categories[i].JunkBytes > res.Categories[j].JunkBytes
	})

	// 组装清理清单
	var light, deep, big []FileItem
	for _, s := range res.Categories {
		for _, f := range s.JunkFiles {
			switch {
			case f.Depth == "light":
				light = append(light, f)
			default:
				deep = append(deep, f)
			}
			if f.Size >= bigFileThreshold && len(big) < maxBigFiles {
				big = append(big, f)
			}
		}
		if len(res.BigFiles) == 0 {
			res.BigFiles = nil
		}
	}
	if len(big) < maxBigFiles {
		for _, s := range res.Categories {
			for _, f := range s.TopFiles {
				if f.Size >= bigFileThreshold && len(big) < maxBigFiles {
					big = append(big, f)
				}
			}
		}
	}
	sort.Slice(big, func(i, j int) bool { return big[i].Size > big[j].Size })

	res.Light = light
	res.Deep = deep
	res.BigFiles = big
	res.ReclaimBytes = res.JunkBytes
	res.ElapsedMs = time.Since(start).Milliseconds()
	return res
}

func mkItem(e fsx.Entry, a classify.Assessment, depth string) FileItem {
	return FileItem{
		Path:       e.Path,
		Name:       filepath.Base(e.Path),
		Size:       e.Size,
		Mod:        e.Mod.Unix(),
		Category:   a.Category,
		Risk:       a.Risk,
		Priority:   a.Priority,
		Reasons:    a.Reasons,
		Selectable: a.Selectable,
		Depth:      depth,
	}
}

// SmartTargets 返回预置的"精准清理"快捷目标（供一键扫描使用）。
func SmartTargets() []string {
	la := os.Getenv("LOCALAPPDATA")
	ad := os.Getenv("APPDATA")
	win := os.Getenv("SystemRoot")
	if win == "" {
		win = `C:\Windows`
	}
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			out = append(out, p)
		}
	}
	add(filepath.Join(la, "Temp"))
	add(filepath.Join(win, "Temp"))
	add(filepath.Join(win, "Prefetch"))
	add(filepath.Join(win, "Logs"))
	add(filepath.Join(win, "Minidump"))
	add(filepath.Join(win, "SoftwareDistribution", "Download"))
	add(filepath.Join(la, "Microsoft", "Windows", "INetCache"))
	add(filepath.Join(la, "Microsoft", "Windows", "Explorer"))
	add(filepath.Join(la, "CrashDumps"))
	add(filepath.Join(la, "Microsoft", "Windows", "WER"))
	add(filepath.Join(la, "Packages"))
	add(filepath.Join(ad, "Microsoft", "Windows", "Recent"))
	return out
}
