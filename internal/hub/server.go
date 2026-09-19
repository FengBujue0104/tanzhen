package hub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/models"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie   = "tanzhen_session"
	sessionTTL      = 7 * 24 * time.Hour
	bcryptCost      = bcrypt.DefaultCost
)

type Server struct {
	Store         *Store
	AdminUser     string
	AdminPassword string // plaintext from env; used for header auth + bcrypt verify
	PublicURL     string
	Static        fs.FS
	mux           *http.ServeMux
	passHash      []byte
	sessions      map[string]time.Time
	sessionsMu    sync.Mutex
}

// Config holds hub server configuration from environment.
type Config struct {
	AdminUser     string
	AdminPassword string
	PublicURL     string
}

// ResolveAdminCreds picks username/password from env.
// Prefer ADMIN_USER + ADMIN_PASSWORD; if ADMIN_PASSWORD unset, fall back to
// ADMIN_TOKEN (migration) then default "changeme".
func ResolveAdminCreds() (user, pass string) {
	user = Env("ADMIN_USER", "admin")
	pass = os.Getenv("ADMIN_PASSWORD")
	if pass == "" {
		pass = os.Getenv("ADMIN_TOKEN")
	}
	if pass == "" {
		pass = "changeme"
	}
	return user, pass
}

func NewServer(store *Store, cfg Config, static fs.FS) *Server {
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcryptCost)
	if err != nil {
		// should never fail for normal passwords
		hash = []byte{}
	}
	s := &Server{
		Store:         store,
		AdminUser:     cfg.AdminUser,
		AdminPassword: cfg.AdminPassword,
		PublicURL:     strings.TrimRight(cfg.PublicURL, "/"),
		Static:        static,
		mux:           http.NewServeMux(),
		passHash:      hash,
		sessions:      make(map[string]time.Time),
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

	s.mux.HandleFunc("POST /api/admin/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/admin/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/admin/me", s.handleMe)

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
		s.mux.HandleFunc("GET /install.sh", s.serveInstallSH)
		s.mux.HandleFunc("GET /install.ps1", s.serveInstallPS1)
		s.mux.HandleFunc("GET /{$}", s.serveIndex)
		s.mux.HandleFunc("GET /admin", s.serveAdmin)
		s.mux.HandleFunc("GET /admin/", s.serveAdmin)
		s.mux.HandleFunc("GET /admin/login", s.serveAdmin)
		s.mux.HandleFunc("GET /admin/login/", s.serveAdmin)
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

func (s *Server) serveAdmin(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.Static, "admin.html")
	if err != nil {
		http.Error(w, "admin not found", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

// serveInstallSH serves install.sh; when ?hub=&token= are present, injects
// defaults so `curl .../install.sh?token=...&hub=... | bash` works alone.
func (s *Server) serveInstallSH(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.Static, "install.sh")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	hubQ := strings.TrimSpace(r.URL.Query().Get("hub"))
	tokQ := strings.TrimSpace(r.URL.Query().Get("token"))
	if hubQ == "" {
		hubQ = s.hubBase(r)
	}
	if tokQ != "" {
		inject := fmt.Sprintf(
			"# --- injected by hub (query params) ---\nHUB_URL=%q\nTOKEN=%q\n# --- end inject ---\n",
			strings.TrimRight(hubQ, "/"), tokQ,
		)
		b = injectAfterShebang(b, inject)
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

func (s *Server) serveInstallPS1(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.Static, "install.ps1")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	hubQ := strings.TrimSpace(r.URL.Query().Get("hub"))
	tokQ := strings.TrimSpace(r.URL.Query().Get("token"))
	if hubQ == "" {
		hubQ = s.hubBase(r)
	}
	if tokQ != "" {
		inject := fmt.Sprintf(
			"# --- injected by hub (query params) ---\n$env:TANZHEN_HUB = %s\n$env:TANZHEN_TOKEN = %s\n# --- end inject ---\n",
			psSingleQuote(strings.TrimRight(hubQ, "/")), psSingleQuote(tokQ),
		)
		b = append([]byte(inject), b...)
		// auto-run when query params present
		auto := fmt.Sprintf("\nInstall-Tanzhen -HubUrl $env:TANZHEN_HUB -Token $env:TANZHEN_TOKEN\n")
		b = append(b, []byte(auto)...)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

func injectAfterShebang(src []byte, inject string) []byte {
	text := string(src)
	if strings.HasPrefix(text, "#!") {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			return []byte(text[:i+1] + inject + text[i+1:])
		}
	}
	return append([]byte(inject), src...)
}

func psSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			jsonErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	if s.sessionOK(r) {
		return true
	}
	// Optional header auth for scripts (password or legacy ADMIN_TOKEN value)
	tok := r.Header.Get("X-Admin-Token")
	if tok == "" {
		tok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if tok != "" && s.AdminPassword != "" && tok == s.AdminPassword {
		return true
	}
	return false
}

func (s *Server) sessionOK(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	exp, ok := s.sessions[c.Value]
	if !ok || time.Now().After(exp) {
		delete(s.sessions, c.Value)
		return false
	}
	// sliding expiry
	s.sessions[c.Value] = time.Now().Add(sessionTTL)
	return true
}

func (s *Server) createSession() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	s.sessionsMu.Lock()
	s.sessions[id] = time.Now().Add(sessionTTL)
	s.sessionsMu.Unlock()
	return id
}

func (s *Server) destroySession(id string) {
	s.sessionsMu.Lock()
	delete(s.sessions, id)
	s.sessionsMu.Unlock()
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, id string) {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   -1,
	})
}

func (s *Server) checkPassword(user, pass string) bool {
	if user != s.AdminUser {
		return false
	}
	if len(s.passHash) == 0 {
		return pass == s.AdminPassword
	}
	return bcrypt.CompareHashAndPassword(s.passHash, []byte(pass)) == nil
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, 400, "bad json")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !s.checkPassword(req.Username, req.Password) {
		jsonErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	sid := s.createSession()
	s.setSessionCookie(w, r, sid)
	jsonOK(w, map[string]any{"status": "ok", "user": s.AdminUser})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.destroySession(c.Value)
	}
	s.clearSessionCookie(w, r)
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	jsonOK(w, map[string]any{"user": s.AdminUser, "authenticated": true})
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

func (s *Server) installCmds(base, token string) (linux, win, installURL, winURL string) {
	base = strings.TrimRight(base, "/")
	q := url.Values{}
	q.Set("hub", base)
	q.Set("token", token)
	installURL = fmt.Sprintf("%s/install.sh?%s", base, q.Encode())
	winURL = fmt.Sprintf("%s/install.ps1?%s", base, q.Encode())
	// Primary: Komari-style one-liner with query params (no bash -s -- args)
	linux = fmt.Sprintf(`curl -fsSL '%s' | bash`, installURL)
	win = fmt.Sprintf(`irm '%s' | iex`, winURL)
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
	linux, win, installURL, winURL := s.installCmds(base, token)
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
		InstallURL: installURL,
		WinURL:     winURL,
		HubURL:     base,
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
	linux, win, installURL, winURL := s.installCmds(base, token)
	jsonOK(w, map[string]any{
		"id": id, "name": name, "token": token, "meta": meta,
		"install_cmd": linux, "win_cmd": win,
		"install_url": installURL, "win_url": winURL,
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
