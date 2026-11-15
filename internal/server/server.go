package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"swiftdl/internal/config"
	"swiftdl/internal/manager"
)

//go:embed web
var webFS embed.FS

// Server 提供 REST 接口与内嵌的前端页面。
type Server struct {
	mgr *manager.Manager
}

// New 创建 HTTP 服务。
func New(mgr *manager.Manager) *Server {
	return &Server{mgr: mgr}
}

// Handler 返回带路由的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("POST /api/probe", s.handleProbe)
	mux.HandleFunc("POST /api/tasks", s.handleAdd)
	mux.HandleFunc("POST /api/tasks/{id}/pause", s.handlePause)
	mux.HandleFunc("POST /api/tasks/{id}/resume", s.handleResume)
	mux.HandleFunc("POST /api/tasks/{id}/retry", s.handleResume)
	mux.HandleFunc("POST /api/tasks/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /api/tasks/{id}/reveal", s.handleReveal)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.handleDelete)

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 前端资源禁止缓存，避免升级后浏览器仍使用旧页面
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		fileServer.ServeHTTP(w, r)
	}))
	return mux
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.State())
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.Config())
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var cfg config.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "配置格式错误: "+err.Error())
		return
	}
	updated, err := s.mgr.UpdateConfig(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	meta, err := s.mgr.Probe(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	var req manager.AddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	task, err := s.mgr.Add(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.withID(w, r, s.mgr.Pause)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.withID(w, r, s.mgr.Resume)
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	s.withID(w, r, s.mgr.Cancel)
}

func (s *Server) handleReveal(w http.ResponseWriter, r *http.Request) {
	s.withID(w, r, s.mgr.Reveal)
}

func (s *Server) withID(w http.ResponseWriter, r *http.Request, fn func(string) error) {
	id := r.PathValue("id")
	if err := fn(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	deleteFile := true
	if v := strings.TrimSpace(r.URL.Query().Get("deleteFile")); v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil {
			deleteFile = parsed
		}
	}
	if err := s.mgr.Remove(id, deleteFile); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
