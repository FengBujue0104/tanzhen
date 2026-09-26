package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/models"
)

// newTestServer builds a hub the way LoadConfig does with no ADMIN_ADDR: the
// admin API rides on the public listener, so tests can drive the whole flow
// through a single handler.
func newTestServer(t *testing.T, static fstest.MapFS) *Server {
	t.Helper()
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "t.db"), StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	srv, err := NewServer(store, Config{
		AdminUser:     "admin",
		AdminPassword: "s3cret",
		AdminToken:    "apitok", // the scripted path, separate from the login password
		PublicURL:     "http://hub.example:8080",
		SessionTTL:    time.Hour,
		ShareAdminAPI: true,
		DataDir:       dir,
	}, static)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}

func TestAdminLoginSessionAndInstallInject(t *testing.T) {
	static := fstest.MapFS{
		"index.html":     &fstest.MapFile{Data: []byte("<html>public</html>")},
		"admin.html":     &fstest.MapFile{Data: []byte("<html>admin</html>")},
		"install.sh":     &fstest.MapFile{Data: []byte("#!/usr/bin/env bash\nHUB_URL=\"${HUB_URL:-}\"\nTOKEN=\"${TOKEN:-}\"\necho ok\n")},
		"install.ps1":    &fstest.MapFile{Data: []byte("function Install-Tanzhen {}\n")},
		"install-hub.sh": &fstest.MapFile{Data: []byte("#!/bin/sh\nPUBLIC_URL=\"${PUBLIC_URL:-}\"\necho ok\n")},
	}
	srv := newTestServer(t, static)
	h := srv.Handler()

	// public index has no auth
	rr := do(t, h, "GET", "/", "", nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "public") {
		t.Fatalf("public index: %d %s", rr.Code, rr.Body.String())
	}

	// admin API without auth
	rr = do(t, h, "GET", "/api/admin/nodes", "", nil)
	if rr.Code != 401 {
		t.Fatalf("expected 401, got %d", rr.Code)
	}

	// login
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "s3cret"})
	rr = do(t, h, "POST", "/api/admin/login", string(body), map[string]string{"Content-Type": "application/json"})
	if rr.Code != 200 {
		t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == SessionCookie {
			cookie = c
			break
		}
	}
	if cookie == nil || !cookie.HttpOnly {
		t.Fatalf("missing httpOnly session cookie: %+v", rr.Result().Cookies())
	}

	// create node with session
	rr = do(t, h, "POST", "/api/admin/nodes", `{"name":"n1"}`,
		map[string]string{"Content-Type": "application/json", "Cookie": cookie.String()})
	if rr.Code != 200 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	cmd, _ := created["install_cmd"].(string)
	urlStr, _ := created["install_url"].(string)
	token, _ := created["token"].(string)
	if token == "" || !strings.Contains(cmd, "curl -fsSL") || !strings.Contains(cmd, "install.sh?") {
		t.Fatalf("bad install fields: %+v", created)
	}
	if !strings.Contains(urlStr, "token=") {
		t.Fatalf("install_url: %s", urlStr)
	}

	// header auth still works
	rr = do(t, h, "GET", "/api/admin/nodes", "", map[string]string{"X-Admin-Token": "apitok"})
	if rr.Code != 200 {
		t.Fatalf("header auth: %d", rr.Code)
	}

	// install.sh inject
	rr = do(t, h, "GET", "/install.sh?hub=http://hub.example:8080&token=abc123", "", nil)
	if rr.Code != 200 {
		t.Fatalf("install.sh: %d", rr.Code)
	}
	out := rr.Body.String()
	if !strings.Contains(out, `TOKEN='abc123'`) || !strings.Contains(out, `HUB_URL='http://hub.example:8080'`) {
		t.Fatalf("inject missing: %s", out)
	}
	// The values must be single-quoted: a hub URL carrying a shell metacharacter
	// would otherwise be evaluated on the target host.
	if strings.Contains(out, `TOKEN="abc123"`) {
		t.Fatalf("token not quoted: %s", out)
	}

	// install-hub.sh inject: a fresh hub defaults its PUBLIC_URL to the hub it
	// was fetched from, and mirrors binaries from it before GitHub.
	rr = do(t, h, "GET", "/install-hub.sh?hub=http://hub.example:8080", "", nil)
	if rr.Code != 200 {
		t.Fatalf("install-hub.sh: %d", rr.Code)
	}
	out = rr.Body.String()
	if !strings.Contains(out, `PUBLIC_URL='http://hub.example:8080'`) ||
		!strings.Contains(out, `TANZHEN_BASE_URL='http://hub.example:8080/releases'`) {
		t.Fatalf("install-hub inject missing: %s", out)
	}

	// logout
	rr = do(t, h, "POST", "/api/admin/logout", "{}", map[string]string{"Cookie": cookie.String()})
	if rr.Code != 200 {
		t.Fatalf("logout: %d", rr.Code)
	}
	rr = do(t, h, "GET", "/api/admin/me", "", map[string]string{"Cookie": cookie.String()})
	if rr.Code != 401 {
		t.Fatalf("expected 401 after logout, got %d", rr.Code)
	}
}

// TestInstallPS1Inject pins the PowerShell injection shape. `irm ... | iex`
// evaluates the downloaded text as a scriptblock, where a param() block must be
// the first statement, so the hub may only prepend $env: assignments and must
// never append an invocation — an appended Install-Tanzhen call would run the
// whole install twice, and any param() in the body would be a syntax error.
func TestInstallPS1Inject(t *testing.T) {
	static := fstest.MapFS{
		"index.html":  &fstest.MapFile{Data: []byte("<html>public</html>")},
		"admin.html":  &fstest.MapFile{Data: []byte("<html>admin</html>")},
		"install.sh":  &fstest.MapFile{Data: []byte("#!/bin/sh\necho ok\n")},
		"install.ps1": &fstest.MapFile{Data: []byte("$HubUrl = if ($env:TANZHEN_HUB) { $env:TANZHEN_HUB } else { '' }\nfunction Install-Tanzhen {}\nInstall-Tanzhen\n")},
	}
	srv := newTestServer(t, static)
	h := srv.Handler()

	rr := do(t, h, "GET", "/install.ps1?hub=http://hub.example:8080&token=abc123", "", nil)
	if rr.Code != 200 {
		t.Fatalf("install.ps1: %d %s", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()

	if !strings.Contains(out, "$env:TANZHEN_HUB = 'http://hub.example:8080'") ||
		!strings.Contains(out, "$env:TANZHEN_TOKEN = 'abc123'") {
		t.Fatalf("inject missing: %s", out)
	}
	// Single-quoted: a double-quoted value would let a hub URL containing $(...)
	// run a subexpression on the target host.
	if strings.Contains(out, `$env:TANZHEN_TOKEN = "abc123"`) {
		t.Fatalf("token not single-quoted: %s", out)
	}
	// The body must stay byte-identical after the injected header, and the
	// script's own trailing invocation must be the only one.
	if got := strings.Count(out, "Install-Tanzhen\n"); got != 1 {
		t.Fatalf("expected exactly one Install-Tanzhen invocation, got %d: %s", got, out)
	}
	// Injection must land ahead of the body, never inside it.
	if strings.Index(out, "$env:TANZHEN_HUB") > strings.Index(out, "function Install-Tanzhen") {
		t.Fatalf("inject placed after the body: %s", out)
	}

	// With no token the script is served verbatim: nothing to inject, and an
	// installer that prompts for a token is not a one-command install.
	rr = do(t, h, "GET", "/install.ps1", "", nil)
	if rr.Code != 200 {
		t.Fatalf("install.ps1 bare: %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "TANZHEN_TOKEN") {
		t.Fatalf("bare install.ps1 injected a token: %s", rr.Body.String())
	}
}

// TestAdminIsolated covers ADMIN_ADDR: the admin tree must serve the SPA and
// the API, and the public tree must not expose it.
func TestAdminIsolated(t *testing.T) {
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>public</html>")},
		"admin.html": &fstest.MapFile{Data: []byte("<html>admin</html>")},
	}
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "t.db"), StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	srv, err := NewServer(store, Config{
		AdminUser:     "admin",
		AdminPassword: "s3cret",
		ShareAdminAPI: false,
	}, static)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// Public tree: the admin route is not registered at all, so the mux answers
	// 404 rather than 401 — an isolated admin API is indistinguishable from a
	// path that never existed.
	if rr := do(t, srv.Handler(), "GET", "/api/admin/nodes", "", nil); rr.Code != 404 {
		t.Fatalf("public tree leaked admin API: %d", rr.Code)
	}
	// admin tree: SPA at / and /admin/
	if rr := do(t, srv.AdminHandler(), "GET", "/", "", nil); !strings.Contains(rr.Body.String(), "admin") {
		t.Fatalf("admin tree root: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, srv.AdminHandler(), "GET", "/admin/", "", nil); !strings.Contains(rr.Body.String(), "admin") {
		t.Fatalf("admin tree /admin/: %d %s", rr.Code, rr.Body.String())
	}
	// admin tree still enforces auth
	if rr := do(t, srv.AdminHandler(), "GET", "/api/admin/nodes", "", nil); rr.Code != 401 {
		t.Fatalf("admin tree auth: %d", rr.Code)
	}
}

// TestDefaultPasswordRefused is the regression test for the review finding that
// the hub used to boot happily on the shipped "changeme" password.
func TestDefaultPasswordRefused(t *testing.T) {
	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("x")}}
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "t.db"), StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := NewServer(store, Config{AdminPassword: "changeme"}, static); err == nil {
		t.Fatal("expected refusal on the default password")
	}
	if _, err := NewServer(store, Config{AdminPassword: "changeme", AllowDefault: true}, static); err != nil {
		t.Fatalf("ALLOW_DEFAULT_PASSWORD: %v", err)
	}
	if _, err := NewServer(store, Config{AdminPassword: ""}, static); err == nil {
		t.Fatal("expected refusal on an empty password")
	}
}

func TestHeartbeatNeedsHeaderToken(t *testing.T) {
	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("x")}}
	srv := newTestServer(t, static)

	if _, _, err := srv.Store.CreateNode("n", models.NodeMeta{}); err != nil {
		t.Fatal(err)
	}
	// A token that exists is accepted; a bad one is not. The query-string form
	// must never be accepted, so a proxy log cannot leak a working credential.
	if rr := do(t, srv.Handler(), "POST", "/api/agent/heartbeat?token=deadbeef",
		`{"cpu_usage":1}`, map[string]string{"Content-Type": "application/json"}); rr.Code != 401 {
		t.Fatalf("query token accepted: %d", rr.Code)
	}
	if rr := do(t, srv.Handler(), "POST", "/api/agent/heartbeat", `{"cpu_usage":1}`, nil); rr.Code != 401 {
		t.Fatalf("missing header accepted: %d", rr.Code)
	}

	id, token, err := srv.Store.CreateNode("n2", models.NodeMeta{})
	if err != nil {
		t.Fatal(err)
	}
	hb := `{"cpu_usage":42,"mem_usage":10,"hostname":"box"}`
	rr := do(t, srv.Handler(), "POST", "/api/agent/heartbeat", hb,
		map[string]string{"Content-Type": "application/json", "X-Agent-Token": token})
	if rr.Code != 200 {
		t.Fatalf("heartbeat: %d %s", rr.Code, rr.Body.String())
	}
	st, err := srv.Store.ListStatus()
	if err != nil || len(st) != 2 {
		t.Fatalf("status: %v %d", err, len(st))
	}
	var found bool
	for _, n := range st {
		if n.ID == id && n.Online && n.Metrics != nil && n.Metrics.CPUUsage == 42 {
			found = true
		}
	}
	if !found {
		t.Fatalf("heartbeat not recorded: %+v", st)
	}
}

func TestResolveAdminCreds(t *testing.T) {
	t.Setenv("ADMIN_USER", "")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("ADMIN_TOKEN", "")
	pass, source := resolveAdminPassword()
	if pass != "changeme" || source != "" {
		t.Fatalf("defaults: %q %q", pass, source)
	}
	t.Setenv("ADMIN_TOKEN", "legacy")
	pass, source = resolveAdminPassword()
	if pass != "legacy" || source != "ADMIN_TOKEN" {
		t.Fatalf("token fallback: %q %q", pass, source)
	}
	t.Setenv("ADMIN_PASSWORD", "newpass")
	pass, source = resolveAdminPassword()
	if pass != "newpass" || source != "ADMIN_PASSWORD" {
		t.Fatalf("password wins: %q %q", pass, source)
	}

	// The default password is only fatal when the operator has not opted in.
	os.Unsetenv("ALLOW_DEFAULT_PASSWORD")
	if _, err := NewServer(nil, Config{AdminPassword: "changeme"}, nil); err == nil {
		t.Fatal("expected ErrDefaultPassword")
	}
}

func TestCSPFor(t *testing.T) {
	html := cspFor("text/html; charset=utf-8")
	if !strings.Contains(html, "default-src 'none'") || !strings.Contains(html, "frame-ancestors 'none'") {
		t.Fatalf("html csp: %s", html)
	}
	if got := cspFor("application/json"); got != "default-src 'none'" {
		t.Fatalf("json csp: %s", got)
	}
}

func TestPublicServesAdminWhenShared(t *testing.T) {
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>public</html>")},
		"admin.html": &fstest.MapFile{Data: []byte("<html>admin-console</html>")},
	}
	srv := newTestServer(t, static)
	h := srv.Handler()
	rr := do(t, h, "GET", "/admin", "", nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "admin-console") {
		t.Fatalf("/admin on public: %d %s", rr.Code, rr.Body.String())
	}
	rr = do(t, h, "GET", "/admin/", "", nil)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "admin-console") {
		t.Fatalf("/admin/ on public: %d %s", rr.Code, rr.Body.String())
	}
}

