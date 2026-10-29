// Package api 提供本地 HTTP 接口与实时事件流（SSE）。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"killthec/internal/clean"
	"killthec/internal/dup"
	"killthec/internal/jobs"
	"killthec/internal/residue"
	"killthec/internal/scan"
	"killthec/internal/store"
	"killthec/internal/winapi"
)

const Version = "1.0.0"

type Server struct {
	mgr    *jobs.Manager
	token  string
	assets fs.FS
	index  []byte
	base   context.Context

	uninst    bool
	uninstDir string

	// OnUninstall 由 main 注入，负责实际的卸载流程。
	OnUninstall func(removeConfig bool)
}

func New(base context.Context, mgr *jobs.Manager, token string, assets fs.FS) (*Server, error) {
	idx, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("缺少内置页面资源: %w", err)
	}
	return &Server{mgr: mgr, token: token, assets: assets, index: idx, base: base}, nil
}

// SetUninstallMode 让界面切换到卸载流程。
func (s *Server) SetUninstallMode(dir string) {
	s.uninst = true
	s.uninstDir = dir
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.serveIndex)
	mux.HandleFunc("GET /assets/", s.serveAsset)
	mux.HandleFunc("GET /api/system", s.auth(s.system))
	mux.HandleFunc("GET /api/config", s.auth(s.getConfig))
	mux.HandleFunc("POST /api/config", s.auth(s.setConfig))
	mux.HandleFunc("POST /api/whitelist/add", s.auth(s.whitelistAdd))
	mux.HandleFunc("POST /api/whitelist/remove", s.auth(s.whitelistRemove))
	mux.HandleFunc("GET /api/fs/list", s.auth(s.fsList))
	mux.HandleFunc("POST /api/scan", s.auth(s.startScan))
	mux.HandleFunc("POST /api/dupes", s.auth(s.startDupes))
	mux.HandleFunc("POST /api/residue", s.auth(s.startResidue))
	mux.HandleFunc("POST /api/clean", s.auth(s.startClean))
	mux.HandleFunc("POST /api/recyclebin/empty", s.auth(s.emptyRecycleBin))
	mux.HandleFunc("POST /api/job/cancel", s.auth(s.cancelJob))
	mux.HandleFunc("GET /api/job", s.auth(s.jobState))
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("POST /api/system/elevate", s.auth(s.elevate))
	mux.HandleFunc("POST /api/reveal", s.auth(s.reveal))
	mux.HandleFunc("POST /api/uninstall/run", s.auth(s.runUninstall))
	return noCache(mux)
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) auth(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-KTC") != s.token {
			writeJSON(w, 403, map[string]any{"error": "无效的本地会话令牌"})
			return
		}
		h(w, r)
	}
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	body := strings.ReplaceAll(string(s.index), "__KTC_TOKEN__", s.token)
	mode := "app"
	if s.uninst {
		mode = "uninstall"
	}
	body = strings.ReplaceAll(body, "__KTC_MODE__", mode)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, body)
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/assets/")
	if p == r.URL.Path || strings.Contains(p, "..") {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(s.assets, p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := mime.TypeByExtension(filepath.Ext(p))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<20))
	err := dec.Decode(v)
	if err == io.EOF {
		return nil // 允许空请求体
	}
	return err
}

// badBody 在请求体非法时给出明确错误，避免静默回退到默认范围。
func badBody(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	writeJSON(w, 400, map[string]any{"error": "请求内容无法解析：" + err.Error()})
	return true
}

// ── 系统信息 ──────────────────────────────────────────────────────────────

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	rbSize, rbCount := winapi.RecycleBinInfo()
	cfg := store.Get()
	writeJSON(w, 200, map[string]any{
		"admin":      winapi.IsAdmin(),
		"version":    Version,
		"volumes":    winapi.ListVolumes(),
		"recycle":    map[string]any{"size": rbSize, "count": rbCount},
		"config":     cfg,
		"dataDir":    store.Dir(),
		"installDir": filepath.Dir(winapi.ExePath()),
		"uninstall":  s.uninst,
		"exe":        winapi.ExePath(),
	})
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, store.Get())
}

type cfgReq struct {
	Theme      *string `json:"theme"`
	LastMode   *string `json:"lastMode"`
	MinDupSize *int64  `json:"minDupSize"`
	SkipSystem *bool   `json:"skipSystem"`
}

func (s *Server) setConfig(w http.ResponseWriter, r *http.Request) {
	var req cfgReq
	if badBody(w, readJSON(r, &req)) {
		return
	}
	cfg, err := store.Update(func(c *store.Config) {
		if req.Theme != nil {
			c.Theme = *req.Theme
		}
		if req.LastMode != nil {
			c.LastMode = *req.LastMode
		}
		if req.MinDupSize != nil && *req.MinDupSize >= 0 {
			c.MinDupSize = *req.MinDupSize
		}
		if req.SkipSystem != nil {
			c.SkipSystem = *req.SkipSystem
		}
	})
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, cfg)
}

type pathReq struct {
	Path string `json:"path"`
	Dir  string `json:"dir"`
}

func (s *Server) whitelistAdd(w http.ResponseWriter, r *http.Request) {
	var req pathReq
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if req.Dir == "" {
		writeJSON(w, 400, map[string]any{"error": "请先选择要加入白名单的文件夹"})
		return
	}
	st, err := os.Stat(req.Dir)
	if err != nil || !st.IsDir() {
		writeJSON(w, 400, map[string]any{"error": "该路径不是有效文件夹"})
		return
	}
	cfg, err := store.AddWhitelist(req.Dir)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, cfg)
}

func (s *Server) whitelistRemove(w http.ResponseWriter, r *http.Request) {
	var req pathReq
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	cfg, err := store.RemoveWhitelist(req.Dir)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, cfg)
}

// ── 目录浏览（应用内文件夹选择器）────────────────────────────────────────

type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s *Server) fsList(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		writeJSON(w, 200, map[string]any{"path": "", "parent": "", "dirs": driveList()})
		return
	}
	p = filepath.Clean(p)
	ents, err := os.ReadDir(p)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "无法读取该目录：" + err.Error()})
		return
	}
	dirs := make([]dirEntry, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		low := strings.ToLower(name)
		if low == "$recycle.bin" || low == "system volume information" {
			continue
		}
		if e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		dirs = append(dirs, dirEntry{Name: name, Path: filepath.Join(p, name)})
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	parent := filepath.Dir(p)
	if parent == p {
		parent = ""
	}
	writeJSON(w, 200, map[string]any{"path": p, "parent": parent, "dirs": dirs})
}

func driveList() []dirEntry {
	var out []dirEntry
	for _, v := range winapi.ListVolumes() {
		out = append(out, dirEntry{Name: strings.TrimSuffix(v.Root, `\`), Path: v.Root})
	}
	return out
}

// ── 扫描 ──────────────────────────────────────────────────────────────────

type scanReq struct {
	Root string `json:"root"`
	Deep bool   `json:"deep"`
}

func (s *Server) startScan(w http.ResponseWriter, r *http.Request) {
	var req scanReq
	if badBody(w, readJSON(r, &req)) {
		return
	}
	if req.Root == "" {
		req.Root = os.Getenv("SystemDrive") + `\`
	}
	j := s.mgr.New(s.base, "scan")
	go func() {
		res := scan.Run(j.Context(), scan.Options{
			Root: req.Root,
			Deep: req.Deep,
			OnProgress: func(p scan.Progress) bool {
				j.Progress(jobs.Event{
					Phase: p.Phase, Current: p.Current, Done: p.Files,
					Bytes: p.Bytes, Elapsed: p.Elapsed, Message: "正在扫描磁盘",
				})
				return j.Context().Err() == nil
			},
		})
		if j.Context().Err() != nil {
			j.Canceled()
			return
		}
		j.Done(res, "扫描完成")
	}()
	writeJSON(w, 200, map[string]any{"job": j.ID, "root": req.Root, "deep": req.Deep})
}

type dupReq struct {
	Roots   []string `json:"roots"`
	Scope   string   `json:"scope"`
	MinSize int64    `json:"minSize"`
}

func (s *Server) startDupes(w http.ResponseWriter, r *http.Request) {
	var req dupReq
	if badBody(w, readJSON(r, &req)) {
		return
	}
	cfg := store.Get()
	if req.MinSize <= 0 {
		req.MinSize = cfg.MinDupSize
	}
	// 范围：显式指定 > scope（personal / drive）> 默认整盘
	roots := req.Roots
	if len(roots) == 0 {
		if req.Scope == "personal" {
			roots = dup.PersonalRoots()
		} else {
			roots = dup.DefaultRoots()
		}
	}
	j := s.mgr.New(s.base, "dupes")
	go func() {
		res := dup.Run(j.Context(), dup.Options{
			Roots:   roots,
			MinSize: req.MinSize,
			OnProgress: func(p dup.Progress) bool {
				j.Progress(jobs.Event{
					Phase: p.Phase, Current: p.Current, Done: p.Scanned,
					Total: p.Total, Elapsed: p.Elapsed, Message: "正在比对文件",
				})
				return j.Context().Err() == nil
			},
		})
		if j.Context().Err() != nil {
			j.Canceled()
			return
		}
		j.Done(res, "重复文件扫描完成")
	}()
	writeJSON(w, 200, map[string]any{"job": j.ID})
}

type residueReq struct {
	Drive string `json:"drive"`
}

func (s *Server) startResidue(w http.ResponseWriter, r *http.Request) {
	var req residueReq
	if badBody(w, readJSON(r, &req)) {
		return
	}
	if req.Drive == "" {
		req.Drive = os.Getenv("SystemDrive")
	}
	j := s.mgr.New(s.base, "residue")
	go func() {
		res := residue.Scan(j.Context(), residue.Options{
			Drive: req.Drive,
			OnProgress: func(p residue.Progress) bool {
				j.Progress(jobs.Event{
					Phase: p.Phase, Current: p.Current, Done: p.Done,
					Total: p.Total, Elapsed: p.Elapsed, Message: "正在检索卸载残留",
				})
				return j.Context().Err() == nil
			},
		})
		if j.Context().Err() != nil {
			j.Canceled()
			return
		}
		j.Done(res, "残留扫描完成")
	}()
	writeJSON(w, 200, map[string]any{"job": j.ID})
}

// ── 清理 ──────────────────────────────────────────────────────────────────

type cleanReq struct {
	Files      []clean.Item `json:"files"`
	Categories []string     `json:"categories"`
	Root       string       `json:"root"`
	Deep       bool         `json:"deep"`
	Method     string       `json:"method"`
	Empty      bool         `json:"emptyAfter"`
	Total      int64        `json:"total"`
	MinSize    int64        `json:"minSize"`
}

func (s *Server) startClean(w http.ResponseWriter, r *http.Request) {
	var req cleanReq
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if len(req.Files) == 0 && len(req.Categories) == 0 {
		writeJSON(w, 400, map[string]any{"error": "没有选择任何待清理的项目"})
		return
	}
	if req.Method != "permanent" {
		req.Method = "recycle"
	}
	j := s.mgr.New(s.base, "clean")
	go func() {
		var res *clean.Result
		if len(req.Categories) > 0 {
			cats := map[string]bool{}
			for _, c := range req.Categories {
				cats[c] = true
			}
			res = clean.RunByCategory(j.Context(), j, clean.CategoryRequest{
				Root: req.Root, Cats: cats, Deep: req.Deep,
				Method: req.Method, MinSize: req.MinSize, KnownTotal: req.Total,
			})
		}
		if len(req.Files) > 0 {
			r2 := clean.Run(j.Context(), j, req.Files, req.Method)
			if res == nil {
				res = r2
			} else {
				res.Deleted += r2.Deleted
				res.Failed += r2.Failed
				res.Skipped += r2.Skipped
				res.FreedBytes += r2.FreedBytes
				res.Errors = append(res.Errors, r2.Errors...)
			}
		}
		if res == nil {
			res = &clean.Result{Method: req.Method}
		}
		if req.Empty && !res.Canceled {
			if _, err := clean.EmptyRecycleBin(); err != nil {
				j.Log("清空回收站失败：" + err.Error())
			}
		}
		if res.Canceled {
			j.Done(res, "清理已取消")
			return
		}
		j.Done(res, "清理完成")
	}()
	writeJSON(w, 200, map[string]any{"job": j.ID, "method": req.Method,
		"count": len(req.Files) + len(req.Categories)})
}

func (s *Server) emptyRecycleBin(w http.ResponseWriter, r *http.Request) {
	before, _ := winapi.RecycleBinInfo()
	if err := winapi.EmptyRecycleBin(); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	after, cnt := winapi.RecycleBinInfo()
	writeJSON(w, 200, map[string]any{"freed": before - after, "remaining": after, "items": cnt})
}

// ── 任务 ──────────────────────────────────────────────────────────────────

func (s *Server) jobState(w http.ResponseWriter, r *http.Request) {
	j := s.mgr.Get(r.URL.Query().Get("id"))
	if j == nil {
		writeJSON(w, 404, map[string]any{"error": "任务不存在或已过期"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"id":     j.ID,
		"kind":   j.Kind,
		"status": j.Status(),
		"error":  j.Err(),
		"last":   j.Last(),
		"result": j.Result(),
	})
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	var req pathReq
	_ = readJSON(r, &req)
	ok := s.mgr.Cancel(req.Path)
	writeJSON(w, 200, map[string]any{"ok": ok})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-KTC") != s.token && r.URL.Query().Get("token") != s.token {
		writeJSON(w, 403, map[string]any{"error": "无效的本地会话令牌"})
		return
	}
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		writeJSON(w, 400, map[string]any{"error": "缺少任务 ID"})
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]any{"error": "当前环境不支持事件流"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fl.Flush()

	ch, cancel := s.mgr.Hub.Subscribe(jobID)
	defer cancel()

	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	enc := json.NewEncoder(w)
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			_, _ = io.WriteString(w, "data: ")
			_ = enc.Encode(e)
			_, _ = io.WriteString(w, "\n")
			fl.Flush()
			if e.Kind == "done" || e.Kind == "error" || e.Kind == "canceled" {
				return
			}
		case <-keep.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// ── 其他 ──────────────────────────────────────────────────────────────────

func (s *Server) elevate(w http.ResponseWriter, r *http.Request) {
	if winapi.IsAdmin() {
		writeJSON(w, 200, map[string]any{"ok": true, "already": true})
		return
	}
	if err := winapi.RunAsAdmin(""); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
	// 提权成功后本进程退出
	go func() {
		time.Sleep(600 * time.Millisecond)
		os.Exit(0)
	}()
}

func (s *Server) reveal(w http.ResponseWriter, r *http.Request) {
	var req pathReq
	_ = readJSON(r, &req)
	if req.Path == "" {
		writeJSON(w, 400, map[string]any{"error": "缺少路径"})
		return
	}
	var err error
	if st, e := os.Stat(req.Path); e == nil && !st.IsDir() {
		err = winapi.ShowInExplorer(req.Path)
	} else {
		err = winapi.OpenPath(req.Path)
	}
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) runUninstall(w http.ResponseWriter, r *http.Request) {
	if s.OnUninstall == nil {
		writeJSON(w, 400, map[string]any{"error": "当前不是卸载模式"})
		return
	}
	var req struct {
		RemoveConfig bool `json:"removeConfig"`
	}
	_ = readJSON(r, &req)
	writeJSON(w, 200, map[string]any{"ok": true, "dir": s.uninstDir})
	go func() {
		time.Sleep(500 * time.Millisecond)
		s.OnUninstall(req.RemoveConfig)
	}()
}

// Serve 在本地回环地址上启动服务，返回实际监听地址。
func Serve(ctx context.Context, srv *http.Server) (string, error) {
	return ServeAddr(ctx, srv, "127.0.0.1:0")
}

// ServeAddr 在指定地址上启动服务。
func ServeAddr(ctx context.Context, srv *http.Server, addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	return ln.Addr().String(), nil
}
