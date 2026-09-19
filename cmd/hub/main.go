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
	publicURL := hub.Env("PUBLIC_URL", "")
	releasesDir := hub.Env("RELEASES_DIR", "./releases")

	adminUser, adminPass := hub.ResolveAdminCreds()

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

	srv := hub.NewServer(store, hub.Config{
		AdminUser:     adminUser,
		AdminPassword: adminPass,
		PublicURL:     publicURL,
	}, static)
	srv.AttachReleases(releasesDir)

	addr := ":" + port
	log.Printf("tanzhen hub listening on %s (data=%s releases=%s admin_user=%s)", addr, dataDir, releasesDir, adminUser)
	if adminPass == "changeme" {
		log.Printf("WARNING: using default admin password 'changeme' — set ADMIN_PASSWORD (or legacy ADMIN_TOKEN)")
	}
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
