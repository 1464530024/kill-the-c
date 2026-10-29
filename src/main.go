// Kill the C —— Windows 磁盘清理工具
// 主程序入口：本地 HTTP 服务 + WebView2 窗口。
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	webview "github.com/jchv/go-webview2"

	"killthec/internal/api"
	"killthec/internal/jobs"
	"killthec/internal/store"
	"killthec/internal/winapi"
)

//go:embed all:web
var webFS embed.FS

const (
	appName   = "Kill the C"
	appDir    = "KillTheC"
	winTitle  = "Kill the C · 磁盘清理专家"
	mutexName = `Global\KillTheC_SingleInstance_Mutex`
)

func main() {
	installDir := flag.String("uninstall-run", "", "内部参数：以卸载模式运行并清理指定目录")
	devPort := flag.Int("dev-port", 0, "内部参数：仅启动本地服务，便于调试")
	uninstall := flag.Bool("uninstall", false, "以卸载模式运行")
	silent := flag.Bool("silent", false, "内部参数：静默执行，不显示界面")
	flag.Parse()

	winapi.InitCOM()
	winapi.EnableDPIAwareness()

	switch {
	case *installDir != "":
		runUninstaller(*installDir, *silent)
	case *uninstall:
		spawnUninstaller(*silent)
	case *devPort > 0:
		runDevServer(*devPort)
	default:
		runApp()
	}
}

// ── 正常启动 ──────────────────────────────────────────────────────────────

func runApp() {
	store.Load()

	rel, first := winapi.SingleInstance(mutexName)
	defer rel()
	if !first {
		winapi.MessageBox(appName, "Kill the C 已经在运行中了。\n请在任务栏中切换到已打开的窗口。", winapi.MB_OK|winapi.MB_ICONINFORMATION)
		return
	}

	token := randToken()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		fatal("内置资源损坏：" + err.Error())
		return
	}

	mgr := jobs.NewManager()
	srv, err := api.New(ctx, mgr, token, sub)
	if err != nil {
		fatal(err.Error())
		return
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	addr, err := api.Serve(ctx, httpSrv)
	if err != nil {
		fatal("无法启动本地服务：" + err.Error())
		return
	}
	url := "http://" + addr + "/"

	if !openWindow(url) {
		// WebView2 不可用时退化为系统浏览器 / Edge 应用窗口
		if !fallbackWindow(url) {
			fatal("未能创建应用窗口。请确认已安装 Microsoft Edge WebView2 运行时。")
			return
		}
	}
}

func openWindow(url string) bool {
	defer func() {
		if r := recover(); r != nil {
			// WebView2 初始化失败，走降级路径
		}
	}()
	var ok bool
	func() {
		defer func() { recover() }()
		w := webview.NewWithOptions(webview.WebViewOptions{
			Debug: false,
			WindowOptions: webview.WindowOptions{
				Title:  winTitle,
				Width:  1200,
				Height: 780,
				IconId: 1,
				Center: true,
			},
		})
		if w == nil {
			return
		}
		defer func() {
			func() { defer func() { recover() }(); w.Destroy() }()
		}()
		hwnd := uintptr(w.Window())
		winapi.ApplyWindowIcon(hwnd, 1)
		if store.Get().Theme == "dark" {
			winapi.SetDarkTitleBar(hwnd, true)
		}
		w.Navigate(url)
		ok = true
		w.Run()
	}()
	return ok
}

func fallbackWindow(url string) bool {
	for _, p := range []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
	} {
		if p == "" || !winapi.PathExists(p) {
			continue
		}
		ud := filepath.Join(os.TempDir(), "ktc-profile")
		_ = os.MkdirAll(ud, 0o755)
		_ = winapi.OpenWithArgs(p, "--app="+url, "--user-data-dir="+ud,
			"--window-size=1200,800", "--no-first-run", "--no-default-browser-check")
		return true
	}
	return false
}

func randToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func fatal(msg string) {
	winapi.MessageBox(appName+" 启动失败", msg, winapi.MB_OK|winapi.MB_ICONERROR)
	os.Exit(1)
}

var _ = fmt.Sprint
