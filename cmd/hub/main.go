package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/hub"
)

//go:embed all:static
var staticRoot embed.FS

func main() {
	log.SetFlags(log.LstdFlags)

	// The hub takes no flags of its own — every knob is an env var, which is
	// what a systemd unit and a compose file already speak. Parsing anyway
	// makes a mistyped flag fail loudly; without it, `--addr :9000` is silently
	// dropped and the hub binds :8080 while the operator believes otherwise.
	flag.Parse()

	cfg := hub.LoadConfig()
	// ADDR wins; PORT is the container-friendly synonym ("8080" -> ":8080").
	addr := envOr("ADDR", ":"+envOr("PORT", "8080"))
	adminAddr := os.Getenv("ADMIN_ADDR")
	dataDir := envOr("DATA_DIR", "./data")
	releasesDir := os.Getenv("RELEASES_DIR")

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("hub: %v", err)
	}

	store, err := hub.NewStore(filepath.Join(dataDir, "tanzhen.db"), cfg.StoreOptions)
	if err != nil {
		log.Fatalf("hub: open store: %v", err)
	}
	defer store.Close()

	static, err := fs.Sub(staticRoot, "static")
	if err != nil {
		log.Fatalf("hub: %v", err)
	}

	srv, err := hub.NewServer(store, cfg, static)
	if err != nil {
		// The only expected failure is a missing/placeholder admin password, so
		// fail loudly rather than booting an unauthenticated admin API.
		log.Fatalf("hub: %v", err)
	}
	defer srv.Close()
	srv.AttachReleases(releasesDir)

	log.Printf("hub: public listener on %s", addr)
	if adminAddr != "" {
		log.Printf("hub: admin listener on %s (isolated)", adminAddr)
	} else {
		log.Printf("hub: no ADMIN_ADDR set; /api/admin/* is served by the public listener")
	}
	if releasesDir == "" {
		log.Printf("hub: RELEASES_DIR not set; agent binaries must be fetched from GitHub Releases")
	}

	// The admin listener gets its own server so its idle timeout and header
	// budget are tuned for interactive use, and so a slow public client cannot
	// tie up admin connections.
	publicSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	adminSrv := &http.Server{
		Addr:              adminAddr,
		Handler:           srv.AdminHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		if err := publicSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	if adminAddr != "" {
		go func() {
			if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		log.Fatalf("hub: listener: %v", err)
	case <-sig:
		log.Printf("hub: shutting down")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if adminAddr != "" {
		_ = adminSrv.Shutdown(ctx)
	}
	if err := publicSrv.Shutdown(ctx); err != nil {
		log.Printf("hub: shutdown: %v", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
