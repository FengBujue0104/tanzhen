package hub

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/models"
	"golang.org/x/crypto/bcrypt"
)

const (
	SessionCookie = "tanzhen_session"
	sweepInterval = 10 * time.Minute
	maxBodyBytes  = 1 << 20 // 1 MiB: every JSON body here is a few hundred bytes
)

// Server holds the hub. The public and admin surfaces are separate handler
// trees so the admin API can be bound to its own listener (ADMIN_ADDR) and
// never ride along on the public port.
type Server struct {
	Store    *Store
	Static   fs.FS
	cfg      Config
	sessions *SessionStore
	limiter  *LoginLimiter

	passHash []byte
	public   *http.ServeMux
	adminMux *http.ServeMux

	// releasesDir, when set, is served under /releases/ so installers can
	// fetch the agent binary. Empty means the route is not mounted.
	releasesDir string

	// DataDir holds appearance assets (background image) and other hub-local
	// files that are not the SQLite database itself.
	DataDir string

	appearanceMu     sync.RWMutex
	appearance       Appearance
	appearanceLoaded bool

	stopSweep chan struct{}
	alerts    *alertState
}

// Config is resolved from the environment by LoadConfig.
type Config struct {
	AdminUser     string
	AdminPassword string
	AdminToken    string // optional static secret for scripts; header auth is off when empty
	PublicURL     string
	TrustProxy    bool
	SessionTTL    time.Duration
	LoginLimit    int
	LoginWindow   time.Duration
	StoreOptions  StoreOptions
	AllowDefault  bool // permit shipping with the built-in "changeme" password
	ShareAdminAPI bool // also mount /api/admin/* on the public listener
	DataDir       string
	// WebhookURL, when set, receives POSTs on node offline and traffic-quota
	// near-full. Empty is a no-op. WebhookTrafficPct is the quota threshold
	// (default 90).
	WebhookURL        string
	WebhookTrafficPct float64
}

// LoadConfig reads the environment.
func LoadConfig() Config {
	pass, source := resolveAdminPassword()
	if source == "" {
		log.Printf("hub: no ADMIN_PASSWORD/ADMIN_TOKEN set; falling back to the built-in default")
	}
	cfg := Config{
		AdminUser:         envOr("ADMIN_USER", "admin"),
		AdminPassword:     pass,
		AdminToken:        os.Getenv("ADMIN_API_TOKEN"),
		PublicURL:         strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"),
		TrustProxy:        envBool("TRUST_PROXY"),
		SessionTTL:        envDuration("SESSION_TTL", 7*24*time.Hour),
		LoginLimit:        envInt("LOGIN_LIMIT", 5),
		LoginWindow:       envDuration("LOGIN_WINDOW", 5*time.Minute),
		AllowDefault:      envBool("ALLOW_DEFAULT_PASSWORD"),
		DataDir:           envOr("DATA_DIR", "./data"),
		WebhookURL:        strings.TrimSpace(os.Getenv("WEBHOOK_URL")),
		WebhookTrafficPct: envFloat("WEBHOOK_TRAFFIC_PCT", 90),
		StoreOptions: StoreOptions{
			OfflineAfter: envDuration("OFFLINE_AFTER", DefaultOfflineAfter),
			HistoryCap:   envInt("HISTORY_POINTS", DefaultHistoryCap),
			PersistEvery: envDuration("HISTORY_PERSIST_EVERY", DefaultPersistEvery),
			Retention:    envDuration("HISTORY_RETENTION", DefaultRetention),
		},
	}
	// With no ADMIN_ADDR there is nowhere else for /api/admin/* to live, so the
	// public listener must serve it. When an admin listener is configured the
	// admin API is isolated to it unless SHARE_ADMIN_API=1 says otherwise.
	if os.Getenv("ADMIN_ADDR") == "" {
		cfg.ShareAdminAPI = true
	} else {
		cfg.ShareAdminAPI = envBool("SHARE_ADMIN_API")
	}
	if cfg.WebhookTrafficPct <= 0 || cfg.WebhookTrafficPct > 100 {
		cfg.WebhookTrafficPct = 90
	}
	return cfg
}

func resolveAdminPassword() (pass, source string) {
	if p := os.Getenv("ADMIN_PASSWORD"); p != "" {
		return p, "ADMIN_PASSWORD"
	}
	if t := os.Getenv("ADMIN_TOKEN"); t != "" {
		return t, "ADMIN_TOKEN"
	}
	return "changeme", ""
}

// ErrDefaultPassword is returned by NewServer when the hub is started with the
// built-in password and the operator has not explicitly opted in.
var ErrDefaultPassword = errors.New("refusing to start with the built-in default admin password " +
	"(set ADMIN_PASSWORD, or ALLOW_DEFAULT_PASSWORD=1 for local testing)")

func NewServer(store *Store, cfg Config, static fs.FS) (*Server, error) {
	if cfg.AdminPassword == "changeme" && !cfg.AllowDefault {
		return nil, ErrDefaultPassword
	}
	if cfg.AdminPassword == "" {
		return nil, errors.New("ADMIN_PASSWORD must not be empty")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = "./data"
	}
	s := &Server{
		Store:     store,
		Static:    static,
		cfg:       cfg,
		sessions:  NewSessionStore(cfg.SessionTTL),
		limiter:   NewLoginLimiter(cfg.LoginLimit, cfg.LoginWindow),
		passHash:  hash,
		DataDir:   dataDir,
		stopSweep: make(chan struct{}),
		alerts:    newAlertState(),
	}

	s.public = s.buildPublic(static)
	s.adminMux = s.buildAdmin(static)

	go s.sweepLoop()
	return s, nil
}

// Handler is the tree served on the public port.
func (s *Server) Handler() http.Handler { return secureHeaders(s.public) }

// AdminHandler is the tree served on the admin port (ADMIN_ADDR). It is a
// complete standalone app: static assets, the SPA, and the admin API.
func (s *Server) AdminHandler() http.Handler { return secureHeaders(s.adminMux) }

func (s *Server) sweepLoop() {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()

	// Offline detection needs a tick on the order of OFFLINE_AFTER, not the
	// 10-minute session/prune interval. A nil channel never fires.
	var wtC <-chan time.Time
	if s.cfg.WebhookURL != "" {
		every := DefaultOfflineAfter
		if s.Store != nil {
			if d := s.Store.OfflineAfter(); d > 0 {
				every = d
			}
		}
		if every > 15*time.Second {
			every = 15 * time.Second
		}
		wt := time.NewTicker(every)
		defer wt.Stop()
		wtC = wt.C
	}

	for {
		select {
		case <-t.C:
			s.sessions.Sweep()
			if s.Store != nil {
				s.Store.PruneSamples()
			}
			s.checkAlerts()
		case <-wtC:
			s.checkAlerts()
		case <-s.stopSweep:
			return
		}
	}
}

// Close stops background goroutines.
func (s *Server) Close() {
	select {
	case <-s.stopSweep:
	default:
		close(s.stopSweep)
	}
}

// ---------------------------------------------------------------------------
// middleware
// ---------------------------------------------------------------------------

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// cspFor returns the policy for HTML documents. Shell scripts and JSON get an
// inert variant so the download endpoints are not constrained.
func cspFor(ctype string) string {
	if strings.HasPrefix(ctype, "text/html") {
		return "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
			"connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	}
	return "default-src 'none'"
}

// sameOriginCORS reflects one explicitly configured origin on the public API.
// There is no wildcard: a monitoring API needs no cross-origin readers, and a
// wildcard would let any page drive the admin surface with a stolen secret.
func (s *Server) sameOriginCORS(next http.Handler) http.Handler {
	origin := s.allowedOrigin()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin != "" && r.Header.Get("Origin") == origin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Agent-Token")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowedOrigin() string {
	if s.cfg.PublicURL == "" {
		return ""
	}
	u, err := url.Parse(s.cfg.PublicURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func limitBody(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next(w, r)
	}
}

// ---------------------------------------------------------------------------
// route tables
// ---------------------------------------------------------------------------

func (s *Server) buildPublic(static fs.FS) *http.ServeMux {
	m := http.NewServeMux()

	m.Handle("GET /api/status", s.sameOriginCORS(http.HandlerFunc(s.handleStatus)))
	m.Handle("GET /api/nodes/{id}/history", s.sameOriginCORS(http.HandlerFunc(s.handleNodeHistory)))
	m.Handle("GET /api/appearance", s.sameOriginCORS(http.HandlerFunc(s.handleGetPublicAppearance)))
	m.Handle("POST /api/agent/heartbeat", s.sameOriginCORS(limitBody(s.handleHeartbeat)))
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	m.HandleFunc("GET /media/background", s.handleMediaBackground)

	if s.cfg.ShareAdminAPI {
		s.registerAdminAPI(m)
	}

	if static != nil {
		files := http.FileServer(http.FS(static))
		m.Handle("GET /assets/", withType(files))
		m.HandleFunc("GET /{$}", s.serveIndex)
		// When admin shares the public listener, the console HTML must live
		// here too — otherwise /admin 404s with no ADMIN_ADDR set (the common
		// one-click install path).
		if s.cfg.ShareAdminAPI {
			m.HandleFunc("GET /admin", s.serveAdmin)
			m.HandleFunc("GET /admin/", s.serveAdmin)
		}
		m.HandleFunc("GET /install.sh", s.serveInstallSH)
		m.HandleFunc("GET /install.ps1", s.serveInstallPS1)
		m.HandleFunc("GET /install-hub.sh", s.serveInstallHub)
	}
	s.mountReleases(m)
	return m
}

func (s *Server) buildAdmin(static fs.FS) *http.ServeMux {
	m := http.NewServeMux()
	s.registerAdminAPI(m)
	// Appearance media is public-ish (status page) but the admin preview also
	// loads it; mount on the isolated admin listener so it works either way.
	m.HandleFunc("GET /media/background", s.handleMediaBackground)
	if static != nil {
		files := http.FileServer(http.FS(static))
		m.Handle("GET /assets/", withType(files))
		m.HandleFunc("GET /{$}", s.serveAdmin)
		m.HandleFunc("GET /admin", s.serveAdmin)
		m.HandleFunc("GET /admin/", s.serveAdmin)
		m.HandleFunc("GET /install.sh", s.serveInstallSH)
		m.HandleFunc("GET /install.ps1", s.serveInstallPS1)
		m.HandleFunc("GET /install-hub.sh", s.serveInstallHub)
	}
	s.mountReleases(m)
	return m
}

// AttachReleases mounts GET /releases/* from a local directory so installers
// can fetch the agent binary. Binaries are public by design (they are also
// published to GitHub Releases) and carry no credentials.
func (s *Server) AttachReleases(dir string) {
	if dir == "" {
		return
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return
	}
	s.releasesDir = dir
	s.mountReleases(s.public)
	s.mountReleases(s.adminMux)
}

func (s *Server) mountReleases(m *http.ServeMux) {
	if s.releasesDir == "" || m == nil {
		return
	}
	m.Handle("GET /releases/", http.StripPrefix("/releases/",
		http.FileServer(http.Dir(s.releasesDir))))
}

const adminAPI = "/api/admin"

func (s *Server) registerAdminAPI(m *http.ServeMux) {
	m.Handle("POST "+adminAPI+"/login", limitBody(s.handleLogin))
	m.Handle("POST "+adminAPI+"/logout", http.HandlerFunc(s.handleLogout))
	m.Handle("GET "+adminAPI+"/me", http.HandlerFunc(s.handleMe))
	m.Handle("GET "+adminAPI+"/nodes", s.admin(s.handleListNodes))
	m.Handle("POST "+adminAPI+"/nodes", s.admin(limitBody(s.handleCreateNode)))
	m.Handle("PATCH "+adminAPI+"/nodes/{id}", s.admin(limitBody(s.handleUpdateNode)))
	m.Handle("DELETE "+adminAPI+"/nodes/{id}", s.admin(s.handleDeleteNode))
	m.Handle("GET "+adminAPI+"/nodes/{id}/install", s.admin(s.handleInstallInfo))
	m.Handle("POST "+adminAPI+"/nodes/{id}/rotate-token", s.admin(s.handleRotateToken))
	m.Handle("GET "+adminAPI+"/appearance", s.admin(s.handleGetAppearance))
	m.Handle("PUT "+adminAPI+"/appearance", s.admin(limitBody(s.handlePutAppearance)))
	// Background upload uses its own MaxBytesReader (8 MiB); skip limitBody.
	m.Handle("POST "+adminAPI+"/appearance/background", s.admin(s.handleUploadBackground))
	m.Handle("DELETE "+adminAPI+"/appearance/background", s.admin(s.handleDeleteBackground))
}

// withType pins the Content-Type Go's FileServer would otherwise guess wrong,
// so assets are never served as text/plain under a strict CSP.
func withType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".css") {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		} else if strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// static + installer endpoints
// ---------------------------------------------------------------------------

// serveFile writes an embedded static file with a no-cache policy and the
// matching CSP.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name, ctype string) {
	b, err := fs.ReadFile(s.Static, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Security-Policy", cspFor(ctype))
	_, _ = w.Write(b)
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	s.serveFile(w, r, "index.html", "text/html; charset=utf-8")
}

func (s *Server) serveAdmin(w http.ResponseWriter, r *http.Request) {
	s.serveFile(w, r, "admin.html", "text/html; charset=utf-8")
}

// serveInstallSH serves install.sh, injecting HUB_URL/TOKEN from the query so
// `curl '.../install.sh?hub=...&token=...' | sh` works as a single command.
// Piped to sh, not bash: the script is POSIX sh by design and a stock Alpine
// has no bash, so the bash form fails there with "bash: not found".
// The values are single-quoted and only assigned when unset, so a caller who
// exported their own HUB_URL keeps it.
func (s *Server) serveInstallSH(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.Static, "install.sh")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	hub := queryOr(r, "hub", s.hubBase(r))
	tok := strings.TrimSpace(r.URL.Query().Get("token"))
	if tok != "" {
		var sb strings.Builder
		sb.WriteString("# --- injected by hub ---\n")
		fmt.Fprintf(&sb, "if [ -z \"${HUB_URL:-}\" ]; then HUB_URL=%s; fi\n", shQuote(hub))
		fmt.Fprintf(&sb, "if [ -z \"${TOKEN:-}\" ]; then TOKEN=%s; fi\n", shQuote(tok))
		appendSHProbeInject(&sb, r)
		b = injectAfterShebang(b, sb.String())
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Security-Policy", cspFor("text/x-shellscript"))
	_, _ = w.Write(b)
}

func (s *Server) serveInstallPS1(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.Static, "install.ps1")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	hub := queryOr(r, "hub", s.hubBase(r))
	tok := strings.TrimSpace(r.URL.Query().Get("token"))

	var out []byte
	if tok != "" {
		// Environment lines only. The script's own variables read them at the
		// top, so there is no appended invocation to drift out of sync with the
		// script body, and no param() block to collide with.
		var sb strings.Builder
		sb.WriteString("# --- injected by hub ---\n")
		fmt.Fprintf(&sb, "$env:TANZHEN_HUB = %s\n", psQuote(hub))
		fmt.Fprintf(&sb, "$env:TANZHEN_TOKEN = %s\n", psQuote(tok))
		appendPSProbeInject(&sb, r)
		sb.WriteString("# --- end inject ---\n\n")
		sb.Write(b)
		out = []byte(sb.String())
	} else {
		out = b
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Security-Policy", cspFor("text/plain"))
	_, _ = w.Write(out)
}

// appendSHProbeInject writes optional probe tunables from the install URL query
// into the POSIX installer. Keys: probe_interval, probe_count, probe_provinces,
// probe_disable. Only assigned when the corresponding env var is unset, so a
// caller who already exported them keeps their choice.
func appendSHProbeInject(sb *strings.Builder, r *http.Request) {
	q := r.URL.Query()
	if v := strings.TrimSpace(q.Get("probe_interval")); v != "" {
		fmt.Fprintf(sb, "if [ -z \"${TANZHEN_PROBE_INTERVAL:-}\" ]; then TANZHEN_PROBE_INTERVAL=%s; fi\n", shQuote(v))
		fmt.Fprintf(sb, "if [ -z \"${TANZHEN_PROBE_EVERY:-}\" ]; then TANZHEN_PROBE_EVERY=%s; fi\n", shQuote(v))
	}
	if v := strings.TrimSpace(q.Get("probe_count")); v != "" {
		fmt.Fprintf(sb, "if [ -z \"${TANZHEN_PROBE_COUNT:-}\" ]; then TANZHEN_PROBE_COUNT=%s; fi\n", shQuote(v))
	}
	if v := strings.TrimSpace(q.Get("probe_provinces")); v != "" {
		fmt.Fprintf(sb, "if [ -z \"${TANZHEN_PROBE_PROVINCES:-}\" ]; then TANZHEN_PROBE_PROVINCES=%s; fi\n", shQuote(v))
	}
	if v := strings.TrimSpace(q.Get("probe_disable")); v == "1" || strings.EqualFold(v, "true") {
		fmt.Fprintf(sb, "if [ -z \"${TANZHEN_PROBE_DISABLE:-}\" ]; then TANZHEN_PROBE_DISABLE=1; fi\n")
	}
}

func appendPSProbeInject(sb *strings.Builder, r *http.Request) {
	q := r.URL.Query()
	if v := strings.TrimSpace(q.Get("probe_interval")); v != "" {
		fmt.Fprintf(sb, "if (-not $env:TANZHEN_PROBE_INTERVAL) { $env:TANZHEN_PROBE_INTERVAL = %s }\n", psQuote(v))
	}
	if v := strings.TrimSpace(q.Get("probe_count")); v != "" {
		fmt.Fprintf(sb, "if (-not $env:TANZHEN_PROBE_COUNT) { $env:TANZHEN_PROBE_COUNT = %s }\n", psQuote(v))
	}
	if v := strings.TrimSpace(q.Get("probe_provinces")); v != "" {
		fmt.Fprintf(sb, "if (-not $env:TANZHEN_PROBE_PROVINCES) { $env:TANZHEN_PROBE_PROVINCES = %s }\n", psQuote(v))
	}
	if v := strings.TrimSpace(q.Get("probe_disable")); v == "1" || strings.EqualFold(v, "true") {
		sb.WriteString("if (-not $env:TANZHEN_PROBE_DISABLE) { $env:TANZHEN_PROBE_DISABLE = '1' }\n")
	}
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// serveInstallHub serves install-hub.sh, injecting this hub's own base URL as
// the default PUBLIC_URL: a second hub redeployed from an existing one (a
// mirror, or a migration) then reports the right address to its agents without
// the operator having to know the IP. TANZHEN_PORT / ADMIN_PASSWORD are read
// from the environment by the script and are never injected.
func (s *Server) serveInstallHub(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.Static, "install-hub.sh")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	hub := queryOr(r, "hub", s.hubBase(r))
	inject := fmt.Sprintf(
		"# --- injected by hub ---\n"+
			"if [ -z \"${PUBLIC_URL:-}\" ]; then PUBLIC_URL=%s; fi\n"+
			"if [ -z \"${TANZHEN_BASE_URL:-}\" ]; then TANZHEN_BASE_URL=%s; fi\n",
		shQuote(hub), shQuote(strings.TrimRight(hub, "/")+"/releases"))
	b = injectAfterShebang(b, inject)
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Security-Policy", cspFor("text/x-shellscript"))
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

func queryOr(r *http.Request, key, def string) string {
	if v := strings.TrimSpace(r.URL.Query().Get(key)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return def
}

// ---------------------------------------------------------------------------
// auth
// ---------------------------------------------------------------------------

// admin gates the admin API. A session cookie is the normal path; a static
// ADMIN_API_TOKEN is the scripted path and is compared in constant time.
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
	if c, err := r.Cookie(SessionCookie); err == nil && s.sessions.Check(c.Value) {
		return true
	}
	if s.cfg.AdminToken == "" {
		return false
	}
	tok := r.Header.Get("X-Admin-Token")
	if tok == "" {
		if v := r.Header.Get("Authorization"); strings.HasPrefix(v, "Bearer ") {
			tok = strings.TrimPrefix(v, "Bearer ")
		}
	}
	if tok == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.AdminToken)) == 1
}

func (s *Server) checkPassword(user, pass string) bool {
	if subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.AdminUser)) != 1 {
		// Still burn a bcrypt comparison so a wrong username costs the same as
		// a wrong password and cannot be distinguished by timing.
		_, _ = bcrypt.GenerateFromPassword([]byte(pass), bcrypt.MinCost)
		return false
	}
	return bcrypt.CompareHashAndPassword(s.passHash, []byte(pass)) == nil
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	key := clientKey(r, s.cfg.TrustProxy)
	if !s.limiter.Allow(key) {
		jsonErr(w, http.StatusTooManyRequests, "too many attempts; try again later")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad json")
		return
	}
	req.Username = strings.TrimSpace(req.Username)

	if !s.checkPassword(req.Username, req.Password) {
		if s.limiter.Fail(key) {
			jsonErr(w, http.StatusTooManyRequests, "too many attempts; try again later")
			return
		}
		// Uniform status: a wrong username and a wrong password are
		// indistinguishable to the client.
		jsonErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.limiter.Reset(key)

	sid := s.sessions.Create()
	if sid == "" {
		jsonErr(w, http.StatusServiceUnavailable, "session table full")
		return
	}
	s.setSessionCookie(w, r, sid)
	jsonOK(w, map[string]any{"status": "ok", "user": s.cfg.AdminUser})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		s.sessions.Revoke(c.Value)
	}
	s.clearSessionCookie(w, r)
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	jsonOK(w, map[string]any{
		"user":          s.cfg.AdminUser,
		"authenticated": true,
		"api_token":     s.cfg.AdminToken != "",
	})
}

// cookieSecure decides the Secure attribute. An explicit COOKIE_SECURE=0/1
// wins; otherwise infer from PUBLIC_URL, then from the request. Defaulting to
// the inferred value keeps plain-HTTP local setups working while the https case
// never serves the session over a cleartext channel.
func (s *Server) cookieSecure(r *http.Request) bool {
	if v, ok := os.LookupEnv("COOKIE_SECURE"); ok {
		return v == "1" || strings.EqualFold(v, "true")
	}
	if s.cfg.PublicURL != "" {
		return strings.HasPrefix(s.cfg.PublicURL, "https://")
	}
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cookieSecure(r),
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cookieSecure(r),
		MaxAge:   -1,
	})
}

// ---------------------------------------------------------------------------
// public API
// ---------------------------------------------------------------------------

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListStatus()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOK(w, map[string]any{
		"nodes":      list,
		"updated_at": time.Now(),
		"server":     "tanzhen",
	})
}

func (s *Server) handleNodeHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ok, err := s.Store.HasNode(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		jsonErr(w, http.StatusNotFound, "not found")
		return
	}

	now := time.Now().Unix()
	ret := int64(s.Store.Retention() / time.Second)
	if ret < 1 {
		ret = int64(DefaultRetention / time.Second)
	}
	from := now - ret
	to := now
	if v := strings.TrimSpace(r.URL.Query().Get("from")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			jsonErr(w, http.StatusBadRequest, "bad from")
			return
		}
		from = n
	}
	if v := strings.TrimSpace(r.URL.Query().Get("to")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			jsonErr(w, http.StatusBadRequest, "bad to")
			return
		}
		to = n
	}
	minT := now - ret
	if from < minT {
		from = minT
	}

	samples, err := s.Store.ListHistory(id, from, to)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if samples == nil {
		samples = []models.Sample{}
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOK(w, map[string]any{"samples": samples})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	// Header only. Accepting the token from the query string would leak it into
	// proxy access logs and shell history on every single report.
	tok := r.Header.Get("X-Agent-Token")
	if tok == "" {
		jsonErr(w, http.StatusUnauthorized, "missing token")
		return
	}
	id, _, meta, err := s.Store.NodeByToken(tok)
	if err != nil {
		jsonErr(w, http.StatusUnauthorized, "invalid token")
		return
	}
	var hb models.Heartbeat
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad json")
		return
	}
	s.Store.SaveHeartbeat(id, &hb, meta)
	if s.cfg.WebhookURL != "" {
		go s.checkAlerts()
	}
	jsonOK(w, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// admin API
// ---------------------------------------------------------------------------

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListNodesAdmin()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]any{"nodes": list})
}

func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	var req models.CreateNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad json")
		return
	}
	id, token, err := s.Store.CreateNode(req.Name, req.Meta)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := req.Name
	if name == "" {
		name = "节点-" + id[:6]
	}
	linux, win, installURL, winURL, agentBin := s.installCmds(s.hubBase(r), token)
	jsonOK(w, models.CreateNodeResponse{
		ID:         id,
		Name:       name,
		Token:      token,
		InstallCmd: linux,
		WinCmd:     win,
		InstallURL: installURL,
		WinURL:     winURL,
		AgentBin:   agentBin,
		HubURL:     s.hubBase(r),
	})
}

func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req models.UpdateNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.Store.UpdateNode(id, req.Name, req.Meta); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonErr(w, http.StatusNotFound, "not found")
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Store.DeleteNode(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonErr(w, http.StatusNotFound, "not found")
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleInstallInfo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name, token, meta, err := s.Store.GetNodeAdmin(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "not found")
		return
	}
	jsonOK(w, s.installPayload(r, id, name, token, meta))
}

func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	token, err := s.Store.RotateToken(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonErr(w, http.StatusNotFound, "not found")
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	name, _, meta, err := s.Store.GetNodeAdmin(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, s.installPayload(r, id, name, token, meta))
}

func (s *Server) installPayload(r *http.Request, id, name, token string, meta models.NodeMeta) map[string]any {
	base := s.hubBase(r)
	linux, win, installURL, winURL, agentBin := s.installCmds(base, token)
	return map[string]any{
		"id": id, "name": name, "token": token, "meta": meta,
		"install_cmd": linux, "win_cmd": win,
		"install_url": installURL, "win_url": winURL,
		"agent_bin": agentBin, "hub_url": base,
	}
}

// ---------------------------------------------------------------------------
// install command generation
// ---------------------------------------------------------------------------

// installCmds builds the one-command installers. The token is carried in the
// query string because that is what makes the single-command form possible; the
// agent reports with X-Agent-Token instead, so the token is not resent on every
// heartbeat. Operators who would rather not paste a secret into a shell should
// use the two-step form shown in the UI.
func (s *Server) installCmds(base, token string) (linux, win, installURL, winURL, agentBin string) {
	base = strings.TrimRight(base, "/")
	q := url.Values{}
	q.Set("hub", base)
	q.Set("token", token)
	installURL = base + "/install.sh?" + q.Encode()
	winURL = base + "/install.ps1?" + q.Encode()
	agentBin = base + "/releases/tanzhen-agent-linux-amd64"
	linux = fmt.Sprintf(`curl -fsSL '%s' | sh`, installURL)
	win = fmt.Sprintf(`irm '%s' | iex`, winURL)
	return
}

func (s *Server) hubBase(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
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

// ---------------------------------------------------------------------------
// json helpers
// ---------------------------------------------------------------------------

func jsonOK(w http.ResponseWriter, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ---------------------------------------------------------------------------
// env helpers
// ---------------------------------------------------------------------------

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	return os.Getenv(key) == "1" || strings.EqualFold(os.Getenv(key), "true")
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envFloat(key string, def float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// randHex returns n random bytes hex-encoded, or "" if the system RNG fails.
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
