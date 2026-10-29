// Package residue 扫描已卸载 / 已删除应用留下的残留文件。
package residue

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"killthec/internal/fsx"
	"killthec/internal/store"
)

var procMultiByteToWideChar = windows.NewLazySystemDLL("kernel32.dll").NewProc("MultiByteToWideChar")

type App struct {
	Name     string
	Publisher string
	Location string
	Uninstall string
	Icon     string
	Version  string
	RegPath  string
	Orphan   bool
}

type Candidate struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Path       string   `json:"path"`
	Kind       string   `json:"kind"` // appdata | programdata | programfiles | registry | startmenu | drive | shortcut
	KindLabel  string   `json:"kindLabel"`
	Size       int64    `json:"size"`
	Files      int64    `json:"files"`
	Mod        int64    `json:"mod"`
	Confidence string   `json:"confidence"` // high | medium | low
	Score      int      `json:"score"`
	Signals    []string `json:"signals"`
	Related    string   `json:"related"`
	Risk       string   `json:"risk"`
	Selectable bool     `json:"selectable"`
}

type Result struct {
	Drive       string      `json:"drive"`
	Apps        int         `json:"apps"`
	Candidates  []Candidate `json:"candidates"`
	TotalBytes  int64       `json:"totalBytes"`
	HighCount   int         `json:"highCount"`
	MediumCount int         `json:"mediumCount"`
	LowCount    int         `json:"lowCount"`
	ElapsedMs   int64       `json:"elapsedMs"`
	Roots       []string    `json:"roots"`
	Errors      []string    `json:"errors"`
}

type Progress struct {
	Phase   string `json:"phase"`
	Current string `json:"current"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Found   int    `json:"found"`
	Elapsed int64  `json:"elapsed"`
}

type Options struct {
	Drive      string
	OnProgress func(Progress) bool
}

// ── 注册表：已安装应用 ────────────────────────────────────────────────────

func uninstallRoots() []struct {
	Key    registry.Key
	Path   string
	Source string
} {
	return []struct {
		Key    registry.Key
		Path   string
		Source string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, "machine"},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`, "machine32"},
		{registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, "user"},
	}
}

// InstalledApps 读取本机已安装应用清单。
func InstalledApps() []App {
	var out []App
	seen := map[string]bool{}
	for _, r := range uninstallRoots() {
		k, err := registry.OpenKey(r.Key, r.Path, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		names, err := k.ReadSubKeyNames(-1)
		k.Close()
		if err != nil {
			continue
		}
		for _, n := range names {
			sk, err := registry.OpenKey(r.Key, r.Path+`\`+n, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			a := App{RegPath: r.Path + `\` + n}
			a.Name = getStr(sk, "DisplayName")
			a.Publisher = getStr(sk, "Publisher")
			a.Location = getStr(sk, "InstallLocation")
			a.Uninstall = getStr(sk, "UninstallString")
			a.Icon = getStr(sk, "DisplayIcon")
			a.Version = getStr(sk, "DisplayVersion")
			sysComp, _, _ := sk.GetIntegerValue("SystemComponent")
			sk.Close()
			if a.Name == "" || sysComp == 1 {
				continue
			}
			key := strings.ToLower(a.Name) + "|" + strings.ToLower(a.Location)
			if seen[key] {
				continue
			}
			seen[key] = true
			if a.Location != "" && !exists(a.Location) {
				a.Orphan = true
			}
			out = append(out, a)
		}
	}
	return out
}

func getStr(k registry.Key, name string) string {
	v, _, err := k.GetStringValue(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func exists(p string) bool {
	p = strings.Trim(p, `"`)
	p = strings.TrimSuffix(p, `\`)
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// ── 名称归一化与匹配 ─────────────────────────────────────────────────────

var tokenRe = regexp.MustCompile(`[^a-z0-9\p{Han}]+`)

var stopWords = map[string]bool{
	"inc": true, "ltd": true, "llc": true, "co": true, "corp": true, "corporation": true,
	"limited": true, "company": true, "software": true, "technologies": true, "technology": true,
	"studio": true, "studios": true, "team": true, "app": true, "apps": true, "desktop": true,
	"setup": true, "installer": true, "updater": true, "update": true, "version": true,
	"the": true, "and": true, "for": true, "pack": true, "manager": true, "client": true,
	"service": true, "services": true, "system": true, "systems": true, "tool": true, "tools": true,
	"network": true, "media": true, "player": true, "reader": true, "editor": true, "game": true,
	"games": true, "launcher": true, "platform": true, "framework": true, "runtime": true,
	"x64": true, "x86": true, "win": true, "windows": true, "microsoft": true,
}

func tokens(s string) []string {
	s = strings.ToLower(s)
	parts := tokenRe.Split(s, -1)
	var out []string
	for _, p := range parts {
		if p == "" || stopWords[p] {
			continue
		}
		out = append(out, p)
	}
	return out
}

// safeSystemDirs 是操作系统或运行时自带的目录，永不判定为残留。
var safeSystemDirs = map[string]bool{
	"microsoft": true, "windows": true, "packages": true, "temp": true, "tmp": true,
	"comms": true, "connecteddevicesplatform": true, "d3dscache": true,
	"elevateddiagnostics": true, "fontcache": true, "grouppolicy": true, "history": true,
	"iconcache": true, "inetcache": true, "peerdistrepub": true,
	"placeholdertilelogofolder": true, "publishers": true, "publishlicense": true,
	"speech": true, "vault": true, "virtualstore": true, "programs": true,
	"crashdumps": true, "desktop": true, "documents": true, "downloads": true,
	"pictures": true, "videos": true, "music": true, "onedrive": true,
	"nvidia": true, "amd": true, "intel": true, "realtek": true, "qualcomm": true,
	"nvidia corporation": true, "intel corporation": true,
	"application data": true, "cache": true, "caches": true, "logs": true,
	"startup": true, "start menu": true, "templates": true, "recent": true,
	"sendto": true, "nethood": true, "printhood": true, "fonts": true,
	"local settings": true, "my documents": true, "temporary internet files": true,
	"common files": true, "microsoft shared": true, "windowsapps": true,
	"windows defender": true, "microsoft edge": true, "microsoft edgewebview": true,
	"microsoft office": true, "microsoft onedrive": true, "microsoft visual studio": true,
	"nvidia geforce experience": true, "realtek semiconductor corp": true,
}

func isSafeName(n string) bool {
	return safeSystemDirs[strings.ToLower(strings.TrimSpace(n))]
}

// 典型的残留物特征
var residueFileHints = []string{
	"cache", "caches", "logs", "log", "temp", "tmp", "crashpad", "crashdumps",
	"blob_storage", "local storage", "session storage", "indexeddb",
	"gpucache", "code cache", "service worker", "update", "updates", "installer",
	"uninstall", "backup", "config", "settings", "profile", "userdata", "user data",
	"cachestorage", "shadercache", "dumps", "reports", "diagnostics",
}

// ── 主流程 ────────────────────────────────────────────────────────────────

func Scan(ctx context.Context, opt Options) *Result {
	start := time.Now()
	res := &Result{Drive: opt.Drive}
	if res.Drive == "" {
		res.Drive = os.Getenv("SystemDrive")
		if res.Drive == "" {
			res.Drive = "C:"
		}
	}
	emit := func(p Progress) {
		if opt.OnProgress != nil {
			opt.OnProgress(p)
		}
	}

	apps := InstalledApps()
	res.Apps = len(apps)

	// 建立"已安装"证据索引：名称、发行商、安装路径片段、Program Files 目录名
	claimedPaths := map[string]bool{}
	known := map[string]bool{}
	addTokens := func(s string) {
		for _, t := range tokens(s) {
			if len(t) >= 2 {
				known[t] = true
			}
		}
	}
	for _, a := range apps {
		if a.Location != "" {
			claimedPaths[strings.ToLower(filepath.Clean(a.Location))] = true
		}
		addTokens(a.Name)
		addTokens(a.Publisher)
		for _, raw := range []string{a.Location, a.Uninstall, a.Icon} {
			for _, seg := range splitPathSegments(raw) {
				seg = strings.TrimSpace(seg)
				seg = strings.TrimSuffix(seg, ".exe")
				if len(seg) >= 3 && !strings.Contains(seg, ":") {
					addTokens(seg)
				}
			}
		}
	}
	// Program Files 下已存在的目录同样视为"已安装"
	for _, pf := range []string{
		filepath.Join(os.Getenv("ProgramFiles")),
		filepath.Join(os.Getenv("ProgramFiles(x86)")),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs"),
	} {
		if pf == "" {
			continue
		}
		if ents, err := os.ReadDir(fsx.LongPath(pf)); err == nil {
			for _, e := range ents {
				if e.IsDir() {
					addTokens(e.Name())
				}
			}
		}
	}

	roots := residueRoots(res.Drive)
	for _, rd := range roots {
		res.Roots = append(res.Roots, rd.Path)
	}

	total := int64(len(roots))
	var done int64
	var mu sync.Mutex
	var cands []Candidate
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup

	for _, root := range roots {
		if ctx.Err() != nil {
			break
		}
		entries, err := os.ReadDir(fsx.LongPath(root.Path))
		if err != nil {
			done++
			continue
		}
		for _, de := range entries {
			if !de.IsDir() {
				continue
			}
			name := de.Name()
			if isSafeName(name) || store.IsWhitelisted(filepath.Join(root.Path, name)) {
				continue
			}
			full := filepath.Join(root.Path, name)
			kind := root.Kind
			kindLabel := root.Label
			risk := "caution"

			// 已安装应用的目录 → 不是残留
			if isClaimed(full, claimedPaths) {
				continue
			}

			wg.Add(1)
			sem <- struct{}{}
			go func(full, name, kind, kindLabel, risk string) {
				defer wg.Done()
				defer func() { <-sem }()
				c := evaluate(ctx, full, name, kind, kindLabel, risk, known, apps)
				if c == nil {
					return
				}
				mu.Lock()
				cands = append(cands, *c)
				mu.Unlock()
				emit(Progress{Phase: "residue", Current: name, Done: done, Total: total,
					Found: len(cands), Elapsed: time.Since(start).Milliseconds()})
			}(full, name, kind, kindLabel, risk)
		}
		done++
		emit(Progress{Phase: "residue", Current: root.Label, Done: done, Total: total,
			Found: len(cands), Elapsed: time.Since(start).Milliseconds()})
	}
	wg.Wait()

	// 注册表孤儿卸载项
	for _, a := range apps {
		if !a.Orphan || a.Location == "" {
			continue
		}
		cands = append(cands, Candidate{
			ID: "reg:" + a.RegPath, Name: a.Name, Path: a.Location,
			Kind: "registry", KindLabel: "注册表孤儿项",
			Confidence: "high", Score: 9,
			Signals: []string{"卸载记录指向的安装目录已不存在", "该应用已不在本机"},
			Risk: "caution", Selectable: true,
		})
	}

	// 快捷方式失效项
	if ctx.Err() == nil {
		found := scanBrokenShortcuts(res.Drive, known)
		cands = append(cands, found...)
	}

	// 指定非系统盘时，扫描根目录下疑似遗留的程序目录
	if ctx.Err() == nil && !strings.EqualFold(strings.TrimSuffix(res.Drive, `\`), os.Getenv("SystemDrive")) {
		cands = append(cands, scanDriveRoot(res.Drive, known)...)
	}

	sort.Slice(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		return cands[i].Size > cands[j].Size
	})
	if len(cands) > 400 {
		cands = cands[:400]
	}
	res.Candidates = cands
	for _, c := range cands {
		res.TotalBytes += c.Size
		switch c.Confidence {
		case "high":
			res.HighCount++
		case "medium":
			res.MediumCount++
		default:
			res.LowCount++
		}
	}
	res.ElapsedMs = time.Since(start).Milliseconds()
	return res
}

type rootDef struct {
	Path  string
	Kind  string
	Label string
}

// splitPathSegments 把注册表里的路径字符串拆成片段，用于构造"已安装"证据词表。
func splitPathSegments(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case '\\', '/', '"', '\'':
			return true
		}
		return false
	})
}

func residueRoots(drive string) []rootDef {
	ad := os.Getenv("APPDATA")
	la := os.Getenv("LOCALAPPDATA")
	pd := os.Getenv("PROGRAMDATA")
	home, _ := os.UserHomeDir()
	var out []rootDef
	add := func(p, kind, label string) {
		if p == "" {
			return
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			out = append(out, rootDef{p, kind, label})
		}
	}
	add(ad, "appdata", "漫游数据 (%APPDATA%)")
	add(la, "localappdata", "本地数据 (%LOCALAPPDATA%)")
	add(filepath.Join(la, "Programs"), "programfiles", "用户安装目录")
	add(pd, "programdata", "程序数据 (%PROGRAMDATA%)")
	add(filepath.Join(home, "Documents"), "docs", "文档目录")
	add(filepath.Join(ad, "Microsoft", "Windows", "Start Menu", "Programs"), "startmenu", "开始菜单")
	add(filepath.Join(pd, "Microsoft", "Windows", "Start Menu", "Programs"), "startmenu_all", "开始菜单（全局）")
	return out
}

func isClaimed(path string, claimed map[string]bool) bool {
	lp := strings.ToLower(filepath.Clean(path))
	for c := range claimed {
		if lp == c || strings.HasPrefix(lp, c+`\`) || strings.HasPrefix(c, lp+`\`) {
			return true
		}
	}
	return false
}

// evaluate 对单个目录打分，判断是否为卸载残留。
func evaluate(ctx context.Context, full, name, kind, kindLabel, risk string, known map[string]bool, apps []App) *Candidate {
	c := &Candidate{
		ID:        full,
		Name:      name,
		Path:      full,
		Kind:      kind,
		KindLabel: kindLabel,
		Confidence: "low",
		Risk:      risk,
		Selectable: true,
	}
	c.Signals = append(c.Signals, "位于"+kindLabel)

	// 名称匹配 → 极可能属于已安装应用，跳过
	tk := tokens(name)
	matched := ""
	for _, t := range tk {
		if known[t] {
			matched = t
			break
		}
	}
	if matched != "" && kind != "programfiles" {
		return nil
	}
	// 与任一已安装应用名高度相似 → 跳过
	for _, a := range apps {
		if simName(name, a.Name) {
			return nil
		}
	}
	if matched == "" {
		c.Score += 2
		c.Signals = append(c.Signals, "目录名未匹配到任何本机已安装应用")
	} else {
		c.Score -= 1
		c.Related = matched
		c.Signals = append(c.Signals, "目录名与部分已安装应用词汇相近，需人工确认")
	}

	// 内容特征
	size, files, hints, lastMod := inspect(ctx, full)
	c.Size, c.Files, c.Mod = size, files, lastMod

	switch kind {
	case "appdata", "localappdata", "programdata":
		c.Score += 2
	case "programfiles":
		c.Score += 1
		c.Risk = "risky"
	case "docs":
		c.Score -= 1
		c.Risk = "risky"
	case "startmenu", "startmenu_all":
		c.Score += 1
	}
	if hints > 0 {
		c.Score += 2
		c.Signals = append(c.Signals, "包含应用缓存 / 配置等典型残留文件")
	}
	if files > 0 && size > 0 && size < 4096 {
		c.Score += 1
		c.Signals = append(c.Signals, "目录接近空壳，仅剩极少量文件")
	}
	if lastMod > 0 && time.Since(time.Unix(lastMod, 0)) > 120*24*time.Hour {
		c.Score += 2
		c.Signals = append(c.Signals, "超过 120 天未被修改")
	}
	if hasVersionDir(full) {
		c.Score += 1
		c.Signals = append(c.Signals, "包含版本号命名的子目录（典型的自动更新残留结构）")
	}
	if size > (2<<30) {
		c.Score -= 3
		c.Signals = append(c.Signals, "体积超过 2GB，请务必确认后清理")
		c.Risk = "risky"
	}

	switch {
	case c.Score >= 6:
		c.Confidence = "high"
	case c.Score >= 3:
		c.Confidence = "medium"
	default:
		c.Confidence = "low"
		c.Selectable = false
	}
	if c.Confidence == "low" {
		return nil
	}
	if len(c.Signals) > 3 {
		c.Signals = c.Signals[:3]
	}
	return c
}

func inspect(ctx context.Context, dir string) (size int64, files int64, hints int, lastMod int64) {
	seen := map[string]bool{}
	depth := strings.Count(dir, string(os.PathSeparator))
	fsx.Walk(ctx, dir, fsx.Options{
		Workers:       4,
		ProgressEvery: 200000,
		SkipDir:       func(p string) bool { return store.IsWhitelisted(p) },
		OnFile: func(e fsx.Entry) {
			files++
			size += e.Size
			if e.Mod.Unix() > lastMod {
				lastMod = e.Mod.Unix()
			}
			if hints < 6 && strings.Count(e.Path, string(os.PathSeparator))-depth <= 2 {
				low := strings.ToLower(e.Path)
				for _, h := range residueFileHints {
					if seen[h] {
						continue
					}
					if strings.Contains(low, h) {
						seen[h] = true
						hints++
						break
					}
				}
			}
		},
	})
	return size, files, hints, lastMod
}

func hasVersionDir(dir string) bool {
	entries, err := os.ReadDir(fsx.LongPath(dir))
	if err != nil {
		return false
	}
	verRe := regexp.MustCompile(`[0-9]+\.[0-9]+`)
	for _, e := range entries {
		if e.IsDir() && verRe.MatchString(e.Name()) {
			return true
		}
	}
	return false
}

func simName(a, b string) bool {
	ta, tb := tokens(a), tokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	inter := 0
	set := map[string]bool{}
	for _, t := range ta {
		set[t] = true
	}
	for _, t := range tb {
		if set[t] {
			inter++
		}
	}
	if inter == 0 {
		return false
	}
	ratio := float64(inter) / float64(len(ta))
	return ratio >= 0.5
}

// ── 快捷方式 ──────────────────────────────────────────────────────────────

func scanBrokenShortcuts(drive string, known map[string]bool) []Candidate {
	ad := os.Getenv("APPDATA")
	pd := os.Getenv("PROGRAMDATA")
	var dirs []string
	if ad != "" {
		dirs = append(dirs, filepath.Join(ad, "Microsoft", "Windows", "Start Menu", "Programs"))
	}
	if pd != "" {
		dirs = append(dirs, filepath.Join(pd, "Microsoft", "Windows", "Start Menu", "Programs"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Desktop"))
	}
	var out []Candidate
	seen := map[string]bool{}
	for _, d := range dirs {
		_ = filepath.WalkDir(fsx.LongPath(d), func(p string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				return nil
			}
			if !strings.EqualFold(filepath.Ext(p), ".lnk") {
				return nil
			}
			if len(out) >= 40 {
				return filepath.SkipAll
			}
			target := lnkTarget(p)
			if target == "" {
				return nil
			}
			target = strings.Trim(strings.TrimSpace(target), `"`)
			if target == "" || exists(target) {
				return nil
			}
			key := strings.ToLower(target)
			if seen[key] {
				return nil
			}
			seen[key] = true
			name := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
			out = append(out, Candidate{
				ID: "lnk:" + p, Name: name, Path: p,
				Kind: "shortcut", KindLabel: "失效快捷方式",
				Confidence: "high", Score: 8,
				Signals:   []string{"快捷方式指向的目标已不存在：" + target},
				Risk:      "safe", Selectable: true,
			})
			return nil
		})
	}
	return out
}

// lnkTarget 解析 Windows 快捷方式的目标路径（宽容实现，失败返回空串）。
func lnkTarget(path string) string {
	b, err := os.ReadFile(fsx.LongPath(path))
	if err != nil || len(b) < 76 {
		return ""
	}
	if binary.LittleEndian.Uint32(b[0:4]) != 0x4C {
		return ""
	}
	flags := binary.LittleEndian.Uint32(b[20:24])
	hasIDList := flags&0x1 != 0
	hasLinkInfo := flags&0x2 != 0
	unicodeFlags := flags&0x80 != 0
	off := 76
	if hasIDList {
		if off+2 > len(b) {
			return ""
		}
		idLen := int(binary.LittleEndian.Uint16(b[off : off+2]))
		off += 2 + idLen
	}
	if !hasLinkInfo || off+8 > len(b) {
		return ""
	}
	liStart := off
	liSize := int(binary.LittleEndian.Uint32(b[liStart : liStart+4]))
	if liSize <= 0 || liStart+liSize > len(b) {
		liSize = len(b) - liStart
	}
	liFlags := binary.LittleEndian.Uint32(b[liStart+8 : liStart+12])
	if liFlags&0x1 == 0 {
		return ""
	}
	lbOff := int(binary.LittleEndian.Uint32(b[liStart+16 : liStart+20]))
	abs := liStart + lbOff
	if abs <= 0 || abs >= liStart+liSize {
		return ""
	}
	seg := b[abs : liStart+liSize]
	if unicodeFlags && looksWide(seg) {
		u := make([]uint16, 0, len(seg)/2)
		for i := 0; i+1 < len(seg); i += 2 {
			c := binary.LittleEndian.Uint16(seg[i : i+2])
			if c == 0 {
				break
			}
			u = append(u, c)
		}
		return string(utf16.Decode(u))
	}
	end := 0
	for end < len(seg) && seg[end] != 0 {
		end++
	}
	return decodeANSI(seg[:end])
}

func looksWide(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	zeros := 0
	half := len(b) / 2
	for i := 1; i < len(b) && i < 40; i += 2 {
		if b[i] == 0 {
			zeros++
		}
	}
	return zeros*2 >= half
}

func decodeANSI(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	const cpACP = 0
	n, _, _ := procMultiByteToWideChar.Call(cpACP, 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0, 0)
	if n == 0 {
		return string(b)
	}
	buf := make([]uint16, n)
	procMultiByteToWideChar.Call(cpACP, 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(n))
	return syscall.UTF16ToString(buf)
}

// scanDriveRoot 对非系统盘根目录做一次疑似遗留目录扫描。
func scanDriveRoot(drive string, known map[string]bool) []Candidate {
	root := strings.TrimSuffix(drive, `\`) + `\`
	entries, err := os.ReadDir(fsx.LongPath(root))
	if err != nil {
		return nil
	}
	var out []Candidate
	for _, de := range entries {
		if !de.IsDir() {
			continue
		}
		name := de.Name()
		if isSafeName(name) || name == "$RECYCLE.BIN" || name == "System Volume Information" {
			continue
		}
		full := filepath.Join(root, name)
		if store.IsWhitelisted(full) {
			continue
		}
		if _, err := os.Stat(filepath.Join(full, "Windows")); err == nil {
			continue // 另一个系统盘
		}
		tk := tokens(name)
		matched := ""
		for _, t := range tk {
			if known[t] {
				matched = t
				break
			}
		}
		if matched != "" {
			continue
		}
		size, files := fsx.DirSize(full)
		sig := []string{"位于 " + strings.TrimSuffix(drive, `\`) + " 盘根目录，且名称未匹配到任何已安装应用"}
		score := 3
		conf := "medium"
		if files == 0 {
			score, conf = 6, "high"
			sig = append(sig, "空目录")
		}
		if hasUninstaller(full) {
			score -= 2
			sig = append(sig, "目录内存在卸载程序，可能仍是有效安装目录")
			conf = "low"
		}
		if conf == "low" {
			continue
		}
		out = append(out, Candidate{
			ID: full, Name: name, Path: full,
			Kind: "drive", KindLabel: strings.TrimSuffix(drive, `\`) + " 盘根目录",
			Size: size, Files: files, Confidence: conf, Score: score,
			Signals: sig, Risk: "risky", Selectable: true,
		})
	}
	return out
}

func hasUninstaller(dir string) bool {
	entries, err := os.ReadDir(fsx.LongPath(dir))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := strings.ToLower(e.Name())
		if strings.HasPrefix(n, "unins") || n == "uninstall.exe" || n == "uninstaller.exe" {
			return true
		}
	}
	return false
}
