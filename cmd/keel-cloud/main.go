// Command keel-cloud runs the Keel Cloud web service.
//
// Configuration (environment):
//
//	KEEL_DATABASE_URL   PostgreSQL URL (required), e.g. postgres://keel:keel@localhost:5432/keel
//	KEEL_ADDR           listen address (default :8080)
//	KEEL_BASE_URL       external base URL, used in invite links (default http://localhost:8080)
//	KEEL_DATA_DIR       working directory for clones and runs (default ./keel-data)
//	KEEL_ENCRYPTION_KEY 64-char hex AES-256 key for values at rest (generated into the
//	                    data dir when unset — set explicitly in production)
//
//	KEEL_GITHUB_APP_ID, KEEL_GITHUB_APP_SLUG, KEEL_GITHUB_PRIVATE_KEY (or
//	KEEL_GITHUB_PRIVATE_KEY_FILE), KEEL_GITHUB_WEBHOOK_SECRET enable the
//	optional GitHub App integration (repo picker, push-triggered deploys).
//
// Docker and git must be available on the host.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/UcGeorge/keel/internal/cloudserver"
	"github.com/UcGeorge/keel/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := cloudserver.ConfigFromEnv()
	srv, err := cloudserver.New(ctx, cfg, version.Version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("keel cloud listening", "addr", cfg.Addr, "base_url", cfg.BaseURL, "version", version.Version)

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	select {
	case err := <-errCh:
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}
}
