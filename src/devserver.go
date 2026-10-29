// 开发调试入口：只启动本地服务，不创建窗口。
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"io/fs"
	"time"

	"killthec/internal/api"
	"killthec/internal/jobs"
	"killthec/internal/store"
	"killthec/internal/winapi"
)

func runDevServer(port int) {
	winapi.InitCOM()
	store.Load()
	token := randToken()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	mgr := jobs.NewManager()
	srv, err := api.New(ctx, mgr, token, sub)
	if err != nil {
		panic(err)
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	addr, err := api.ServeAddr(ctx, httpSrv, fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		panic(err)
	}
	fmt.Printf("DEV http://%s/  token=%s\n", addr, token)
	// 令牌写到程序同目录，方便脚本读取
	if exe, err := os.Executable(); err == nil {
		_ = os.WriteFile(exe+".token", []byte(token), 0o644)
	}
	select {}
}
