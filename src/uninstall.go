// 卸载流程：把自身复制到临时目录后执行清理，避免"自己删自己"。
package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"

	"killthec/internal/api"
	"killthec/internal/jobs"
	"killthec/internal/store"
	"killthec/internal/winapi"
)

const uninstMutex = `Global\KillTheC_Uninstall_Mutex`

// spawnUninstaller 复制自身到临时目录并启动卸载模式。
func spawnUninstaller(silent bool) {
	self := winapi.ExePath()
	if self == "" {
		fatal("无法定位程序自身路径")
		return
	}
	dir := filepath.Dir(self)
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("KillTheC-Uninst-%d.exe", time.Now().UnixNano()))
	if err := copyFile(self, tmp); err != nil {
		fatal("无法准备卸载程序：" + err.Error())
		return
	}
	args := []string{"--uninstall-run", dir}
	if silent {
		args = append(args, "--silent")
	}
	cmd := exec.Command(tmp, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	if err := cmd.Start(); err != nil {
		fatal("无法启动卸载程序：" + err.Error())
		return
	}
	os.Exit(0)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// runUninstaller 以卸载界面运行。
func runUninstaller(dir string, silent bool) {
	if silent {
		doUninstall(dir, false)
		return
	}
	rel, first := winapi.SingleInstance(uninstMutex)
	defer rel()
	if !first {
		winapi.MessageBox(appName, "卸载程序已经在运行中。", winapi.MB_OK|winapi.MB_ICONINFORMATION)
		return
	}

	store.Load()
	token := randToken()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		fatal(err.Error())
		return
	}
	mgr := jobs.NewManager()
	srv, err := api.New(ctx, mgr, token, sub)
	if err != nil {
		fatal(err.Error())
		return
	}
	srv.SetUninstallMode(dir)
	srv.OnUninstall = func(removeConfig bool) {
		doUninstall(dir, removeConfig)
		time.Sleep(3200 * time.Millisecond)
		os.Exit(0)
	}

	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	addr, err := api.Serve(ctx, httpSrv)
	if err != nil {
		fatal(err.Error())
		return
	}
	url := "http://" + addr + "/"
	if !openWindow(url) {
		// 无窗口时退化为静默卸载
		doUninstall(dir, false)
		winapi.MessageBox(appName, "Kill the C 已从本机卸载完成。", winapi.MB_OK|winapi.MB_ICONINFORMATION)
		os.Exit(0)
	}
}

// doUninstall 执行实际的卸载动作。
func doUninstall(dir string, removeConfig bool) {
	// 1) 结束仍在运行的主程序
	winapi.KillProcessesByName("KillTheC.exe")
	time.Sleep(400 * time.Millisecond)

	// 2) 删除快捷方式
	for _, lnk := range shortcutPaths() {
		if winapi.PathExists(lnk) {
			_, _ = winapi.DeletePermanent(lnk)
		}
	}

	// 3) 删除控制面板卸载项
	_ = registry.DeleteKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Uninstall\KillTheC`)
	_ = registry.DeleteKey(registry.LOCAL_MACHINE,
		`Software\Microsoft\Windows\CurrentVersion\Uninstall\KillTheC`)

	// 4) 可选：删除用户配置
	if removeConfig {
		_, _ = winapi.DeletePermanent(store.Dir())
	}

	// 5) 删除安装目录
	if dir != "" && winapi.PathExists(dir) {
		_, _ = winapi.DeletePermanent(dir)
	}

	// 6) 清理自身
	self := winapi.ExePath()
	_ = winapi.MarkSelfDeleteOnReboot(self)
	_, _ = winapi.DeletePermanent(self)
}

func shortcutPaths() []string {
	var out []string
	home, _ := os.UserHomeDir()
	if home != "" {
		out = append(out, filepath.Join(home, "Desktop", appName+".lnk"))
		out = append(out, filepath.Join(home, "Desktop", "Kill the C.lnk"))
	}
	ad := os.Getenv("APPDATA")
	if ad != "" {
		out = append(out, filepath.Join(ad, "Microsoft", "Windows", "Start Menu", "Programs", appName+".lnk"))
		out = append(out, filepath.Join(ad, "Microsoft", "Windows", "Start Menu", "Programs", "Kill the C", appName+".lnk"))
	}
	pd := os.Getenv("PUBLIC")
	if pd != "" {
		out = append(out, filepath.Join(pd, "Desktop", appName+".lnk"))
	}
	// 去重
	seen := map[string]bool{}
	var uniq []string
	for _, p := range out {
		k := strings.ToLower(p)
		if !seen[k] {
			seen[k] = true
			uniq = append(uniq, p)
		}
	}
	return uniq
}
