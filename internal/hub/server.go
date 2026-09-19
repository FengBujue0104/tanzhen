package hub

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/models"
)

type Server struct {
	Store      *Store
	AdminToken string
	PublicURL  string
	Static     fs.FS
	mux        *http.ServeMux
}

func NewServer(store *Store, adminToken, publicURL string, static fs.FS) *Server {
	s := &Server{
		Store:      store,
		AdminToken: adminToken,
		PublicURL:  strings.TrimRight(publicURL, "/"),
		Static:     static,
		mux:        http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return cors(s.mux)
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Admin-Token, X-Agent-Token")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("POST /api/agent/heartbeat", s.handleHeartbeat)
	s.mux.HandleFunc("GET /api/admin/nodes", s.admin(s.handleListNodes))
	s.mux.HandleFunc("POST /api/admin/nodes", s.admin(s.handleCreateNode))
	s.mux.HandleFunc("PATCH /api/admin/nodes/{id}", s.admin(s.handleUpdateNode))
	s.mux.HandleFunc("DELETE /api/admin/nodes/{id}", s.admin(s.handleDeleteNode))
	s.mux.HandleFunc("GET /api/admin/nodes/{id}/install", s.admin(s.handleInstallInfo))
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	if s.Static != nil {
		fileServer := http.FileServer(http.FS(s.Static))
		s.mux.Handle("GET /assets/", fileServer)
		s.mux.HandleFunc("GET /install.sh", s.serveStaticFile("install.sh", "text/x-shellscript; charset=utf-8"))
		s.mux.HandleFunc("GET /install.ps1", s.serveStaticFile("install.ps1", "text/plain; charset=utf-8"))
		s.mux.HandleFunc("GET /{$}", s.serveIndex)
		s.mux.HandleFunc("GET /admin", s.serveIndex)
		s.mux.HandleFunc("GET /admin/", s.serveIndex)
	}
}

func (s *Server) serveStaticFile(name, ctype string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(s.Static, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	}
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.Static, "index.html")
	if err != nil {
		http.Error(w, "index not found", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Admin-Token")
		if tok == "" {
			tok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if s.AdminToken == "" || tok != s.AdminToken {
			jsonErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (s *Server) hubBase(r *http.Request) string {
	if s.PublicURL != "" {
		return s.PublicURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost:8080"
	}
	return scheme + "://" + host
}

func (s *Server) installCmds(base, token string) (linux, win string) {
	linux = fmt.Sprintf(`curl -fsSL %s/install.sh | bash -s -- --hub %s --token %s`, base, base, token)
	win = fmt.Sprintf(`irm %s/install.ps1 | iex; Install-Tanzhen -HubUrl '%s' -Token '%s'`, base, base, token)
	return
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListStatus()
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w, map[string]any{
		"nodes":      list,
		"updated_at": time.Now(),
		"server":     "tanzhen",
	})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	tok := r.Header.Get("X-Agent-Token")
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	if tok == "" {
		jsonErr(w, 401, "missing token")
		return
	}
	id, _, err := s.Store.NodeByToken(tok)
	if err != nil {
		jsonErr(w, 401, "invalid token")
		return
	}
	var hb models.Heartbeat
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		jsonErr(w, 400, "bad json")
		return
	}
	s.Store.SaveHeartbeat(id, &hb)
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListNodesAdmin()
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w, map[string]any{"nodes": list})
}

func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	var req models.CreateNodeRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	id, token, err := s.Store.CreateNode(req.Name, req.Meta)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	base := s.hubBase(r)
	linux, win := s.installCmds(base, token)
	name := req.Name
	if name == "" {
		name = "节点-" + id[:6]
	}
	jsonOK(w, models.CreateNodeResponse{
		ID:         id,
		Name:       name,
		Token:      token,
		InstallCmd: linux,
		WinCmd:     win,
	})
}

func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req models.UpdateNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, 400, "bad json")
		return
	}
	if err := s.Store.UpdateNode(id, req.Name, req.Meta); err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Store.DeleteNode(id); err != nil {
		jsonErr(w, 404, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleInstallInfo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name, token, meta, err := s.Store.GetNodeAdmin(id)
	if err != nil {
		jsonErr(w, 404, "not found")
		return
	}
	base := s.hubBase(r)
	linux, win := s.installCmds(base, token)
	jsonOK(w, map[string]any{
		"id": id, "name": name, "token": token, "meta": meta,
		"install_cmd": linux, "win_cmd": win,
		"hub_url": base,
	})
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// AttachReleases mounts GET /releases/* from a local directory (optional).
func (s *Server) AttachReleases(dir string) {
	if dir == "" {
		return
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return
	}
	s.mux.Handle("GET /releases/", http.StripPrefix("/releases/", http.FileServer(http.Dir(dir))))
}
