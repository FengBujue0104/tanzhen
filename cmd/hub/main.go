package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/FengBujue0104/tanzhen/internal/hub"
)

//go:embed all:static
var staticRoot embed.FS

func main() {
	port := hub.Env("PORT", "8080")
	dataDir := hub.Env("DATA_DIR", "./data")
	adminToken := hub.Env("ADMIN_TOKEN", "changeme")
	publicURL := hub.Env("PUBLIC_URL", "")
	releasesDir := hub.Env("RELEASES_DIR", "./releases")

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "tanzhen.db")
	store, err := hub.NewStore(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	static, err := fs.Sub(staticRoot, "static")
	if err != nil {
		log.Fatal(err)
	}

	srv := hub.NewServer(store, adminToken, publicURL, static)
	srv.AttachReleases(releasesDir)

	addr := ":" + port
	log.Printf("tanzhen hub listening on %s (data=%s releases=%s)", addr, dataDir, releasesDir)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
