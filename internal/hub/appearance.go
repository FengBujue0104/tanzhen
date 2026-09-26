package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxBackgroundBytes = 8 << 20 // 8 MiB
	appearanceFileName = "appearance.json"
	appearanceSubDir   = "appearance"
)

// Appearance holds public status-page background settings.
type Appearance struct {
	Enabled      bool   `json:"enabled"`
	Dim          int    `json:"dim"`           // 0–100 dark wash over image
	PanelOpacity int    `json:"panel_opacity"` // 0–100 card/panel opacity
	Fit          string `json:"fit"`           // cover | contain
	Position     string `json:"position"`      // e.g. center
	// BackgroundExt is the on-disk extension including dot, e.g. ".webp". Empty = no image.
	BackgroundExt string `json:"background_ext,omitempty"`
}

func defaultAppearance() Appearance {
	return Appearance{
		Enabled:      false,
		Dim:          40,
		PanelOpacity: 92,
		Fit:          "cover",
		Position:     "center",
	}
}

func (s *Server) appearanceDir() string {
	return filepath.Join(s.DataDir, appearanceSubDir)
}

func (s *Server) appearanceJSONPath() string {
	return filepath.Join(s.appearanceDir(), appearanceFileName)
}

func (s *Server) backgroundPath(ext string) string {
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return filepath.Join(s.appearanceDir(), "bg"+ext)
}

func (s *Server) loadAppearance() Appearance {
	s.appearanceMu.RLock()
	cached := s.appearance
	loaded := s.appearanceLoaded
	s.appearanceMu.RUnlock()
	if loaded {
		return cached
	}
	return s.reloadAppearance()
}

func (s *Server) reloadAppearance() Appearance {
	s.appearanceMu.Lock()
	defer s.appearanceMu.Unlock()
	a := defaultAppearance()
	b, err := os.ReadFile(s.appearanceJSONPath())
	if err == nil {
		_ = json.Unmarshal(b, &a)
	}
	a = normalizeAppearance(a)
	s.appearance = a
	s.appearanceLoaded = true
	return a
}

func (s *Server) saveAppearance(a Appearance) error {
	a = normalizeAppearance(a)
	if err := os.MkdirAll(s.appearanceDir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.appearanceJSONPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.appearanceJSONPath()); err != nil {
		return err
	}
	s.appearanceMu.Lock()
	s.appearance = a
	s.appearanceLoaded = true
	s.appearanceMu.Unlock()
	return nil
}

func normalizeAppearance(a Appearance) Appearance {
	if a.Dim < 0 {
		a.Dim = 0
	}
	if a.Dim > 100 {
		a.Dim = 100
	}
	if a.PanelOpacity < 0 {
		a.PanelOpacity = 0
	}
	if a.PanelOpacity > 100 {
		a.PanelOpacity = 100
	}
	switch strings.ToLower(a.Fit) {
	case "contain":
		a.Fit = "contain"
	default:
		a.Fit = "cover"
	}
	if strings.TrimSpace(a.Position) == "" {
		a.Position = "center"
	}
	if a.BackgroundExt != "" && !strings.HasPrefix(a.BackgroundExt, ".") {
		a.BackgroundExt = "." + a.BackgroundExt
	}
	return a
}

func (a Appearance) publicMap() map[string]any {
	out := map[string]any{
		"enabled":        a.Enabled && a.BackgroundExt != "",
		"dim":            a.Dim,
		"panel_opacity":  a.PanelOpacity,
		"fit":            a.Fit,
		"position":       a.Position,
		"has_background": a.BackgroundExt != "",
	}
	if a.Enabled && a.BackgroundExt != "" {
		out["background_url"] = "/media/background"
	} else {
		out["background_url"] = nil
	}
	return out
}

func (s *Server) handleGetPublicAppearance(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, s.loadAppearance().publicMap())
}

func (s *Server) handleGetAppearance(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, s.loadAppearance().publicMap())
}

func (s *Server) handlePutAppearance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled      *bool   `json:"enabled"`
		Dim          *int    `json:"dim"`
		PanelOpacity *int    `json:"panel_opacity"`
		Fit          *string `json:"fit"`
		Position     *string `json:"position"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, 400, "bad json")
		return
	}
	a := s.loadAppearance()
	if req.Enabled != nil {
		a.Enabled = *req.Enabled
	}
	if req.Dim != nil {
		a.Dim = *req.Dim
	}
	if req.PanelOpacity != nil {
		a.PanelOpacity = *req.PanelOpacity
	}
	if req.Fit != nil {
		a.Fit = *req.Fit
	}
	if req.Position != nil {
		a.Position = *req.Position
	}
	if err := s.saveAppearance(a); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w, a.publicMap())
}

func (s *Server) handleUploadBackground(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBackgroundBytes+512*1024)
	if err := r.ParseMultipartForm(maxBackgroundBytes + 512*1024); err != nil {
		jsonErr(w, 400, "file too large or bad multipart (max 8MB)")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		file, hdr, err = r.FormFile("background")
	}
	if err != nil {
		jsonErr(w, 400, "missing file field (file)")
		return
	}
	defer file.Close()

	ext, reader, err := detectImageExt(hdr.Filename, hdr.Header.Get("Content-Type"), file)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}

	if err := os.MkdirAll(s.appearanceDir(), 0o755); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}

	a := s.loadAppearance()
	// remove previous image if extension differs
	if a.BackgroundExt != "" && a.BackgroundExt != ext {
		_ = os.Remove(s.backgroundPath(a.BackgroundExt))
	}

	dest := s.backgroundPath(ext)
	tmp := dest + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	n, err := io.Copy(out, io.LimitReader(reader, maxBackgroundBytes+1))
	_ = out.Close()
	if err != nil {
		_ = os.Remove(tmp)
		jsonErr(w, 500, err.Error())
		return
	}
	if n > maxBackgroundBytes {
		_ = os.Remove(tmp)
		jsonErr(w, 400, "file too large (max 8MB)")
		return
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		jsonErr(w, 500, err.Error())
		return
	}

	a.BackgroundExt = ext
	a.Enabled = true
	if err := s.saveAppearance(a); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w, a.publicMap())
}

func detectImageExt(filename, contentType string, r io.Reader) (ext string, reader io.Reader, err error) {
	sniff := make([]byte, 512)
	n, _ := io.ReadFull(r, sniff)
	sniff = sniff[:n]
	reader = io.MultiReader(bytes.NewReader(sniff), r)
	detected := http.DetectContentType(sniff)
	ctype := contentType
	if ctype == "" || ctype == "application/octet-stream" {
		ctype = detected
	}
	if mt, _, e := mime.ParseMediaType(ctype); e == nil {
		ctype = mt
	}
	switch ctype {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/webp":
		ext = ".webp"
	default:
		lower := strings.ToLower(filename)
		switch {
		case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
			if detected == "image/jpeg" || looksLikeJPEG(sniff) {
				ext = ".jpg"
			}
		case strings.HasSuffix(lower, ".png"):
			if detected == "image/png" || looksLikePNG(sniff) {
				ext = ".png"
			}
		case strings.HasSuffix(lower, ".webp"):
			if detected == "image/webp" || looksLikeWebP(sniff) {
				ext = ".webp"
			}
		}
		// last resort: trust magic only
		if ext == "" {
			switch detected {
			case "image/jpeg":
				ext = ".jpg"
			case "image/png":
				ext = ".png"
			case "image/webp":
				ext = ".webp"
			}
		}
	}
	if ext == "" {
		return "", nil, fmt.Errorf("unsupported image type (JPEG/PNG/WebP only)")
	}
	return ext, reader, nil
}

func looksLikeJPEG(b []byte) bool {
	return len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff
}

func looksLikePNG(b []byte) bool {
	return len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n"
}

func looksLikeWebP(b []byte) bool {
	return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
}

func (s *Server) handleDeleteBackground(w http.ResponseWriter, r *http.Request) {
	a := s.loadAppearance()
	if a.BackgroundExt != "" {
		_ = os.Remove(s.backgroundPath(a.BackgroundExt))
	}
	a.BackgroundExt = ""
	a.Enabled = false
	if err := s.saveAppearance(a); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w, a.publicMap())
}

func (s *Server) handleMediaBackground(w http.ResponseWriter, r *http.Request) {
	a := s.loadAppearance()
	if a.BackgroundExt == "" {
		http.NotFound(w, r)
		return
	}
	path := s.backgroundPath(a.BackgroundExt)
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctype := "application/octet-stream"
	switch a.BackgroundExt {
	case ".jpg", ".jpeg":
		ctype = "image/jpeg"
	case ".png":
		ctype = "image/png"
	case ".webp":
		ctype = "image/webp"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeContent(w, r, "background"+a.BackgroundExt, st.ModTime(), f)
}

