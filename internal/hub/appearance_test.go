package hub

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func appearanceStatic() fstest.MapFS {
	return fstest.MapFS{
		"index.html":  &fstest.MapFile{Data: []byte("<html>public</html>")},
		"admin.html":  &fstest.MapFile{Data: []byte("<html>admin</html>")},
		"install.sh":  &fstest.MapFile{Data: []byte("#!/bin/sh\n")},
		"install.ps1": &fstest.MapFile{Data: []byte("")},
	}
}

func appearanceLoginCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rr := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "s3cret"})
	req := httptest.NewRequest("POST", "/api/admin/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 0, G: 113, B: 227, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAppearancePublicDefault(t *testing.T) {
	srv := newTestServer(t, appearanceStatic())
	h := srv.Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/appearance", nil))
	if rr.Code != 200 {
		t.Fatalf("code %d", rr.Code)
	}
	var m map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &m)
	if m["enabled"] != false {
		t.Fatalf("expected disabled: %+v", m)
	}
	if m["background_url"] != nil {
		t.Fatalf("expected nil url: %+v", m)
	}
}

func TestAppearanceUploadPutDelete(t *testing.T) {
	srv := newTestServer(t, appearanceStatic())
	h := srv.Handler()
	dir := srv.DataDir
	cookie := appearanceLoginCookie(t, h)

	// unauthorized upload
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/admin/appearance/background", nil))
	if rr.Code != 401 {
		t.Fatalf("expected 401, got %d", rr.Code)
	}

	pngBytes := tinyPNG(t)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", "bg.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(pngBytes); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	rr = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/appearance/background", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(cookie)
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("upload: %d %s", rr.Code, rr.Body.String())
	}
	var up map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &up)
	if up["enabled"] != true || up["background_url"] != "/media/background" {
		t.Fatalf("upload resp: %+v", up)
	}
	if _, err := os.Stat(filepath.Join(dir, "appearance", "bg.png")); err != nil {
		t.Fatalf("file missing: %v", err)
	}

	// media
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/media/background", nil))
	if rr.Code != 200 {
		t.Fatalf("media: %d", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Content-Type"), "image/png") {
		t.Fatalf("ctype: %s", rr.Header().Get("Content-Type"))
	}
	got, _ := io.ReadAll(rr.Body)
	if len(got) < 50 {
		t.Fatalf("tiny media body")
	}

	// put settings
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", "/api/admin/appearance", strings.NewReader(`{"dim":55,"panel_opacity":80,"fit":"contain","enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("put: %d %s", rr.Code, rr.Body.String())
	}
	var put map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &put)
	if int(put["dim"].(float64)) != 55 || put["fit"] != "contain" {
		t.Fatalf("put resp: %+v", put)
	}

	// public reflects
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/appearance", nil))
	var pub map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &pub)
	if pub["enabled"] != true || int(pub["dim"].(float64)) != 55 {
		t.Fatalf("public: %+v", pub)
	}

	// delete
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", "/api/admin/appearance/background", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/media/background", nil))
	if rr.Code != 404 {
		t.Fatalf("expected 404 after delete, got %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/appearance", nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &pub)
	if pub["enabled"] != false {
		t.Fatalf("should be disabled: %+v", pub)
	}
}

func TestAppearanceRejectNonImage(t *testing.T) {
	srv := newTestServer(t, appearanceStatic())
	h := srv.Handler()
	cookie := appearanceLoginCookie(t, h)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, _ := w.CreateFormFile("file", "x.txt")
	_, _ = fw.Write([]byte("not an image"))
	_ = w.Close()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/appearance/background", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(cookie)
	h.ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Fatalf("expected 400, got %d %s", rr.Code, rr.Body.String())
	}
}
