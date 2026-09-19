package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestAdminLoginSessionAndInstallInject(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>public</html>")},
		"admin.html": &fstest.MapFile{Data: []byte("<html>admin</html>")},
		"install.sh": &fstest.MapFile{Data: []byte("#!/usr/bin/env bash\nHUB_URL=\"${HUB_URL:-}\"\nTOKEN=\"${TOKEN:-}\"\necho ok\n")},
		"install.ps1": &fstest.MapFile{Data: []byte("function Install-Tanzhen {}\n")},
	}

	srv := NewServer(store, Config{
		AdminUser:     "admin",
		AdminPassword: "s3cret",
		PublicURL:     "http://hub.example:8080",
	}, static)
	h := srv.Handler()

	// public index has no auth
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "public") {
		t.Fatalf("public index: %d %s", rr.Code, rr.Body.String())
	}

	// admin API without auth
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/admin/nodes", nil))
	if rr.Code != 401 {
		t.Fatalf("expected 401, got %d", rr.Code)
	}

	// login
	rr = httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "s3cret"})
	req := httptest.NewRequest("POST", "/api/admin/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
			break
		}
	}
	if cookie == nil || !cookie.HttpOnly {
		t.Fatalf("missing httpOnly session cookie: %+v", rr.Result().Cookies())
	}

	// create node with session
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/admin/nodes", strings.NewReader(`{"name":"n1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	h.ServeHTTP(rr, req)
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
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/admin/nodes", nil)
	req.Header.Set("X-Admin-Token", "s3cret")
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("header auth: %d", rr.Code)
	}

	// install.sh inject
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/install.sh?hub=http://hub.example:8080&token=abc123", nil))
	if rr.Code != 200 {
		t.Fatalf("install.sh: %d", rr.Code)
	}
	out, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(out), `TOKEN="abc123"`) || !strings.Contains(string(out), `HUB_URL="http://hub.example:8080"`) {
		t.Fatalf("inject missing: %s", out)
	}

	// logout
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/admin/logout", strings.NewReader("{}"))
	req.AddCookie(cookie)
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("logout: %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/admin/me", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("expected 401 after logout, got %d", rr.Code)
	}
}

func TestResolveAdminCreds(t *testing.T) {
	os.Unsetenv("ADMIN_USER")
	os.Unsetenv("ADMIN_PASSWORD")
	os.Unsetenv("ADMIN_TOKEN")
	u, p := ResolveAdminCreds()
	if u != "admin" || p != "changeme" {
		t.Fatalf("defaults: %s %s", u, p)
	}
	t.Setenv("ADMIN_TOKEN", "legacy")
	_, p = ResolveAdminCreds()
	if p != "legacy" {
		t.Fatalf("token fallback: %s", p)
	}
	t.Setenv("ADMIN_PASSWORD", "newpass")
	_, p = ResolveAdminCreds()
	if p != "newpass" {
		t.Fatalf("password wins: %s", p)
	}
}
