// Package classify 提供文件分类、优先级与风险评估规则。
package classify

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 风险等级
const (
	RiskSafe      = "safe"      // 安全：系统与个人数据均无影响
	RiskCaution   = "caution"   // 谨慎：可能影响使用体验或包含个人数据
	RiskRisky     = "risky"     // 高风险：可能是程序或系统组件
	RiskProtected = "protected" // 受保护：系统关键区域，禁止清理
)

// 优先级（数值越小越优先）
const (
	PriCritical = 0 // P0 立即可清理
	PriHigh     = 1 // P1 建议清理
	PriMedium   = 2 // P2 视情况清理
	PriLow      = 3 // P3 保守起见不清理
)

// Category 描述一个文件大类。
type Category struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Desc     string `json:"desc"`
	Icon     string `json:"icon"`
	Risk     string `json:"risk"`
	Priority int    `json:"priority"`
}

// Categories 是全部类别的有序定义。
var Categories = []Category{
	{"temp", "临时文件", "系统与程序运行过程产生的临时文件，可安全清理", "temp", RiskSafe, PriCritical},
	{"cache", "缓存文件", "浏览器 / 应用缓存，清理后首次打开会重新生成", "cache", RiskSafe, PriCritical},
	{"log", "日志文件", "运行日志与崩溃记录，一般无保留价值", "log", RiskSafe, PriHigh},
	{"dump", "转储文件", "内存转储与调试文件，单个文件往往很大", "dump", RiskSafe, PriHigh},
	{"backup", "备份与副本", "旧版本备份、临时副本、残留 .bak / .old", "backup", RiskCaution, PriHigh},
	{"update", "更新残留", "系统更新缓存与旧系统目录，删除后无法回滚更新", "update", RiskCaution, PriHigh},
	{"installer", "安装包", "下载或缓存的安装程序，安装完成后通常可删", "installer", RiskCaution, PriMedium},
	{"video", "视频文件", "体积最大的常见文件类型，请确认后再清理", "video", RiskCaution, PriLow},
	{"image", "图片文件", "照片与截图，清理前请确认已备份", "image", RiskCaution, PriLow},
	{"audio", "音频文件", "音乐与录音", "audio", RiskCaution, PriLow},
	{"archive", "压缩包", "打包文件，解压后可按需清理", "archive", RiskCaution, PriMedium},
	{"document", "文档文件", "办公文档与 PDF，请谨慎对待", "document", RiskCaution, PriLow},
	{"code", "开发文件", "源码、依赖与构建产物，清理可能影响编译", "code", RiskCaution, PriMedium},
	{"program", "程序文件", "可执行文件与动态库，删除会导致程序不可用", "program", RiskRisky, PriLow},
	{"system", "系统文件", "操作系统核心文件，绝对禁止清理", "system", RiskProtected, PriLow},
	{"other", "其他文件", "未归类的文件", "other", RiskCaution, PriLow},
}

var catIndex = func() map[string]Category {
	m := map[string]Category{}
	for _, c := range Categories {
		m[c.Key] = c
	}
	return m
}()

// Lookup 按 key 取类别定义。
func Lookup(key string) Category {
	if c, ok := catIndex[key]; ok {
		return c
	}
	return catIndex["other"]
}

var extCat = map[string]string{}

func init() {
	reg := func(cat string, exts ...string) {
		for _, e := range exts {
			extCat[e] = cat
		}
	}
	reg("temp", ".tmp", ".temp", ".~tmp", ".part", ".partial", ".crdownload", ".download", ".!ut", ".cache", ".dmp.tmp")
	reg("log", ".log", ".log1", ".log2", ".etl", ".evtx", ".trace", ".tracelog", ".wer", ".mdmp", ".hdmp", ".stackdump")
	reg("dump", ".dmp", ".core", ".mdmp", ".hdmp", ".vhdmp")
	reg("backup", ".bak", ".old", ".orig", ".backup", ".sav", ".save", ".gho", ".wbcat", ".bkf", ".rbf", ".swp", ".swo")
	reg("update", ".mum", ".manifest", ".cab", ".msu", ".msp")
	reg("installer", ".exe", ".msi", ".msix", ".appx", ".appxbundle", ".msixbundle", ".paf", ".dll.old")
	reg("video", ".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".rmvb", ".rm", ".webm", ".m4v", ".mpg", ".mpeg", ".ts", ".3gp", ".vob", ".f4v")
	reg("image", ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp", ".tif", ".tiff", ".heic", ".raw", ".cr2", ".nef", ".arw", ".psd", ".ai", ".svg", ".ico")
	reg("audio", ".mp3", ".wav", ".flac", ".aac", ".m4a", ".ogg", ".wma", ".ape", ".mid", ".midi", ".cue")
	reg("archive", ".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz", ".iso", ".img", ".tgz", ".zst", ".cabx", ".wim", ".esd")
	reg("document", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".pdf", ".txt", ".md", ".rtf", ".csv", ".odt", ".ods", ".odp", ".epub", ".mobi", ".wps", ".et", ".dps", ".one", ".vsd", ".vsdx", ".xmind")
	reg("code", ".go", ".js", ".ts", ".tsx", ".jsx", ".py", ".java", ".class", ".jar", ".c", ".h", ".cpp", ".hpp", ".cs", ".rb", ".php", ".rs", ".swift", ".kt", ".scala", ".json", ".xml", ".yml", ".yaml", ".toml", ".gradle", ".pom", ".lock", ".min.js", ".min.css", ".map", ".a", ".o", ".obj", ".lib", ".pdb", ".apk", ".ipa")
	reg("program", ".dll", ".sys", ".drv", ".ocx", ".scr", ".com", ".ocx2", ".bin", ".dat", ".pak", ".node", ".so", ".dylib", ".msixbundle2")
	reg("system", ".efi", ".mui", ".mun", ".cat", ".inf", ".pnf", ".nls", ".ttf", ".otf", ".fon", ".theme", ".msi.cab", ".winmd", ".policy", ".mof", ".admx", ".adml", ".reg", ".man", ".msstyles")
	for _, e := range []string{".sys", ".drv", ".efi", ".mui", ".mun", ".cat", ".inf", ".pnf"} {
		extCat[e] = "system"
	}
	// 系统关键后缀优先级最高：覆盖上面 program 的登记
	for _, e := range []string{".dll"} {
		extCat[e] = "program"
	}
}

// 系统保护区：整棵子树都不允许清理
var protectedPrefixes = []string{
	`c:\windows\system32`,
	`c:\windows\syswow64`,
	`c:\windows\winsxs`,
	`c:\windows\assembly`,
	`c:\windows\servicing`,
	`c:\windows\boot`,
	`c:\windows\fonts`,
	`c:\windows\ime`,
	`c:\windows\speech`,
	`c:\windows\systemapps`,
	`c:\windows\immersivecontrolpanel`,
	`c:\windows\microsoft.net`,
	`c:\windows\policydefinitions`,
	`c:\windows\schemas`,
	`c:\windows\diagnostics`,
	`c:\windows\help`,
	`c:\windows\web`,
	`c:\windows\resources`,
	`c:\windows\globalization`,
	`c:\windows\csc`,
	`c:\windows\registration`,
	`c:\windows\systemresources`,
	`c:\windows\system32\config`,
	`c:\$recycle.bin`,
	`c:\system volume information`,
	`c:\recovery`,
	`c:\perflogs`,
	`c:\msocache`,
	`c:\onedrivetemp`,
	`c:\boot`,
}

// 垃圾目录片段：命中即可安全清理
var junkDirFrags = []struct {
	frag   string
	cat    string
	risk   string
	pri    int
	reason string
}{
	{`\appdata\local\temp`, "temp", RiskSafe, PriCritical, "用户临时目录"},
	{`\windows\temp`, "temp", RiskSafe, PriCritical, "系统临时目录"},
	{`\windows\prefetch`, "cache", RiskSafe, PriHigh, "系统预读缓存"},
	{`\softwaredistribution\download`, "update", RiskCaution, PriHigh, "Windows 更新下载缓存"},
	{`\softwaredistribution\datastore`, "update", RiskSafe, PriHigh, "更新数据库缓存"},
	{`\windows\logs`, "log", RiskSafe, PriHigh, "系统日志目录"},
	{`\windows\livekernelreports`, "dump", RiskSafe, PriHigh, "内核报告"},
	{`\windows\minidump`, "dump", RiskSafe, PriHigh, "蓝屏小型转储"},
	{`\windows\memory.dmp`, "dump", RiskSafe, PriHigh, "内存转储文件"},
	{`\windows\winsat`, "log", RiskSafe, PriHigh, "性能评估日志"},
	{`\windows\infsetupapi`, "log", RiskSafe, PriMedium, "驱动安装日志"},
	{`\crashdumps`, "dump", RiskSafe, PriHigh, "程序崩溃转储"},
	{`\wersvc\reportqueue`, "dump", RiskSafe, PriHigh, "错误报告队列"},
	{`\wersvc\reportarchive`, "dump", RiskSafe, PriHigh, "错误报告归档"},
	{`\microsoft\windows\wer`, "dump", RiskSafe, PriHigh, "Windows 错误报告"},
	{`\microsoft\windows\temporary internet files`, "cache", RiskSafe, PriCritical, "IE 缓存"},
	{`\microsoft\windows\inetcache`, "cache", RiskSafe, PriCritical, "网络缓存"},
	{`\microsoft\windows\webcache`, "cache", RiskSafe, PriCritical, "Web 缓存"},
	{`\microsoft\windows\explorer\thumbcache`, "cache", RiskSafe, PriCritical, "缩略图缓存"},
	{`\microsoft\windows\explorer\iconcache`, "cache", RiskSafe, PriCritical, "图标缓存"},
	{`\microsoft\edge\user data\default\cache`, "cache", RiskSafe, PriCritical, "Edge 缓存"},
	{`\microsoft\edge\user data\default\code cache`, "cache", RiskSafe, PriCritical, "Edge 代码缓存"},
	{`\microsoft\edge\user data\default\gpucache`, "cache", RiskSafe, PriCritical, "Edge GPU 缓存"},
	{`\google\chrome\user data\default\cache`, "cache", RiskSafe, PriCritical, "Chrome 缓存"},
	{`\google\chrome\user data\default\code cache`, "cache", RiskSafe, PriCritical, "Chrome 代码缓存"},
	{`\google\chrome\user data\default\gpucache`, "cache", RiskSafe, PriCritical, "Chrome GPU 缓存"},
	{`\360se6\user data`, "cache", RiskSafe, PriCritical, "360 浏览器缓存"},
	{`\tencent\qqbrowser\user data`, "cache", RiskCaution, PriHigh, "QQ 浏览器缓存"},
	{`\packages\localcache`, "cache", RiskSafe, PriHigh, "UWP 应用缓存"},
	{`\deliveryoptimization\cache`, "cache", RiskSafe, PriHigh, "传递优化缓存"},
	{`\package cache`, "installer", RiskCaution, PriMedium, "安装包缓存（删除后修复安装需重新下载）"},
	{`\windows.old`, "update", RiskCaution, PriCritical, "旧系统备份（可回收大量空间，删除后无法回退系统）"},
	{`\windows\softwaredistribution\download`, "update", RiskCaution, PriHigh, "更新缓存"},
	{`\temp\`, "temp", RiskSafe, PriCritical, "临时目录"},
	{`\tmp\`, "temp", RiskSafe, PriCritical, "临时目录"},
	{`\cache\`, "cache", RiskSafe, PriHigh, "缓存目录"},
	{`\caches\`, "cache", RiskSafe, PriHigh, "缓存目录"},
	{`\logs\`, "log", RiskSafe, PriHigh, "日志目录"},
}

// personalDirs 个人数据目录，默认低优先级
var personalDirs = []string{
	`\desktop`, `\documents`, `\pictures`, `\videos`, `\music`,
	`\onedrive`, `\downloads`, `\favorites`, `\saved games`, `\contacts`,
}

// programDirs 程序安装目录
var programDirs = []string{
	`c:\program files`, `c:\program files (x86)`, `\appdata\local\programs`,
}

// Assessment 是对单个文件的分类与风险评估结果。
type Assessment struct {
	Category   string   `json:"category"`
	Risk       string   `json:"risk"`
	Priority   int      `json:"priority"`
	Reasons    []string `json:"reasons"`
	Selectable bool     `json:"selectable"`
	Junk       bool     `json:"junk"`
}

// Ext 返回小写扩展名。
func Ext(name string) string {
	e := strings.ToLower(filepath.Ext(name))
	if len(e) > 32 {
		e = e[:32]
	}
	return e
}

// CatByExt 仅按扩展名归类。
func CatByExt(name string) string {
	e := Ext(name)
	if c, ok := extCat[e]; ok {
		return c
	}
	// 无扩展名或双扩展名（如 .tar.gz）兜底
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tar.bz2") {
		return "archive"
	}
	if e == "" {
		return "other"
	}
	return "other"
}

// Assess 综合路径、大小、修改时间给出分类与风险评级。
func Assess(path string, size int64, mod time.Time) Assessment {
	a := Assessment{Priority: PriLow, Risk: RiskCaution, Category: "other"}

	lp := strings.ToLower(filepath.Clean(path))
	name := filepath.Base(path)
	a.Category = CatByExt(name)

	inWindows := strings.HasPrefix(lp, `c:\windows\`)
	inProgram := strings.HasPrefix(lp, `c:\program files\`) || strings.HasPrefix(lp, `c:\program files (x86)\`)

	// 1) 系统保护区
	for _, p := range protectedPrefixes {
		if lp == p || strings.HasPrefix(lp, p+`\`) {
			a.Risk = RiskProtected
			a.Selectable = false
			a.Priority = PriLow
			a.Reasons = append(a.Reasons, "位于系统保护区，禁止清理")
			a.Category = "system"
			return a
		}
	}

	// 2) 系统根目录下的关键文件
	if strings.Count(lp, `\`) <= 2 {
		base := strings.ToLower(name)
		switch base {
		case "pagefile.sys", "hiberfil.sys", "swapfile.sys", "bootmgr", "bootnxt", "dumpstack.log.tmp":
			a.Risk = RiskProtected
			a.Selectable = false
			a.Category = "system"
			a.Reasons = append(a.Reasons, "系统运行必需文件，仅作展示")
			return a
		}
	}

	// 3) 回收站
	if strings.HasPrefix(lp, `c:\$recycle.bin\`) {
		a.Category = "backup"
		a.Risk = RiskSafe
		a.Priority = PriCritical
		a.Junk = true
		a.Selectable = true
		a.Reasons = append(a.Reasons, "回收站内容，可用「清空回收站」统一释放")
		return a
	}

	// 4) 垃圾目录指纹
	for _, j := range junkDirFrags {
		if strings.Contains(lp, j.frag) {
			a.Category = j.cat
			a.Risk = j.risk
			a.Priority = j.pri
			a.Junk = true
			a.Selectable = true
			a.Reasons = append(a.Reasons, j.reason)
			break
		}
	}

	// 5) 扩展名带来的硬性风险
	if a.Category == "system" || extIsSystem(Ext(name)) {
		a.Risk = RiskProtected
		a.Selectable = false
		a.Category = "system"
		if len(a.Reasons) == 0 {
			a.Reasons = append(a.Reasons, "系统组件文件，禁止清理")
		}
		return a
	}

	// 6) 高价值垃圾类型：临时 / 日志 / 转储 / 崩溃
	if !a.Junk {
		switch a.Category {
		case "temp":
			a.Risk, a.Priority, a.Junk, a.Selectable = RiskSafe, PriCritical, true, true
			a.Reasons = append(a.Reasons, "临时文件（*.tmp）")
		case "log":
			a.Risk, a.Priority, a.Junk, a.Selectable = RiskSafe, PriHigh, true, true
			a.Reasons = append(a.Reasons, "日志文件")
		case "dump":
			a.Risk, a.Priority, a.Junk, a.Selectable = RiskSafe, PriHigh, true, true
			a.Reasons = append(a.Reasons, "内存转储文件")
		case "backup":
			a.Priority = PriHigh
			a.Selectable = true
			a.Risk = RiskCaution
			a.Reasons = append(a.Reasons, "备份 / 副本文件（*.bak / *.old）")
		case "update":
			a.Priority = PriHigh
			a.Selectable = true
			a.Risk = RiskCaution
			a.Reasons = append(a.Reasons, "系统更新缓存，删除后无法从缓存回滚")
		}
	}

	// 7) 旧文件的陈旧度加成
	if a.Junk && size > 50<<20 && time.Since(mod) > 180*24*time.Hour {
		if a.Priority > PriHigh {
			a.Priority = PriHigh
		}
		a.Reasons = append(a.Reasons, "体积较大且超过半年未修改")
	}

	// 8) 个人数据与程序目录降级
	if !a.Junk {
		for _, d := range personalDirs {
			if strings.Contains(lp, d+`\`) {
				if a.Risk != RiskRisky {
					a.Risk = RiskCaution
				}
				a.Priority = PriLow
				a.Selectable = true
				a.Reasons = append(a.Reasons, "位于个人数据目录，请确认后清理")
				break
			}
		}
		for _, d := range programDirs {
			if strings.HasPrefix(lp, d) {
				a.Risk = RiskRisky
				a.Selectable = true
				a.Priority = PriLow
				a.Reasons = append(a.Reasons, "位于程序安装目录，删除可能导致程序异常")
				break
			}
		}
	}

	// 9) Windows 目录内未被指纹命中的内容
	if inWindows && !a.Junk && a.Risk != RiskRisky {
		a.Risk = RiskRisky
		a.Selectable = false
		a.Priority = PriLow
		if len(a.Reasons) == 0 {
			a.Reasons = append(a.Reasons, "Windows 目录内的非垃圾文件，默认不参与清理")
		}
	}

	// 10) 大文件提示
	if !a.Junk && size >= 1<<30 {
		a.Reasons = append(a.Reasons, "超大文件（超过 1GB），请重点确认")
		if a.Priority > PriMedium {
			a.Priority = PriMedium
		}
	}

	if a.Category == "program" && a.Risk != RiskRisky {
		a.Risk = RiskRisky
		a.Selectable = true
		a.Priority = PriLow
		if len(a.Reasons) == 0 {
			a.Reasons = append(a.Reasons, "可执行文件 / 动态库，删除可能破坏程序")
		}
	}

	if len(a.Reasons) == 0 {
		a.Reasons = append(a.Reasons, "常规文件")
	}
	if !a.Junk {
		a.Selectable = a.Risk != RiskProtected
	}
	_ = inProgram
	return a
}

func extIsSystem(e string) bool {
	switch e {
	case ".sys", ".drv", ".efi", ".mui", ".mun", ".cat", ".pnf", ".winmd", ".msstyles":
		return true
	}
	return false
}

// RiskLabel 返回中文风险标签。
func RiskLabel(r string) string {
	switch r {
	case RiskSafe:
		return "安全"
	case RiskCaution:
		return "谨慎"
	case RiskRisky:
		return "高风险"
	case RiskProtected:
		return "受保护"
	}
	return "未知"
}

// PriLabel 返回优先级标签。
func PriLabel(p int) string {
	switch p {
	case PriCritical:
		return "P0"
	case PriHigh:
		return "P1"
	case PriMedium:
		return "P2"
	}
	return "P3"
}

// OnSystemDrive 判断路径是否在系统盘根下。
func OnSystemDrive(p string) bool {
	root := strings.ToUpper(os.Getenv("SystemDrive"))
	if root == "" {
		root = "C:"
	}
	return strings.HasPrefix(strings.ToUpper(filepath.Clean(p)), root)
}
