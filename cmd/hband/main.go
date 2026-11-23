// Command hband is the hband web server.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sshubham2/hband/internal/config"
	"github.com/sshubham2/hband/internal/reports"
	"github.com/sshubham2/hband/internal/server"
	"github.com/sshubham2/hband/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := realMain(ctx, os.Getenv, os.Stderr)
	stop()
	os.Exit(code)
}

// realMain returns the exit code: 2 for invalid configuration, 1 for any
// other startup failure, 0 after a clean shutdown. It never logs the client
// secret, codes, state, tokens or cookie values.
func realMain(ctx context.Context, getenv func(string) string, stderr io.Writer) int {
	log := slog.New(slog.NewTextHandler(stderr, nil))

	cfg, err := config.Load(getenv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Error("cannot create the data directory", "error", err)
		return 1
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "hband.db"))
	if err != nil {
		log.Error("cannot open the database", "error", err)
		return 1
	}
	defer st.Close()

	// The report library creates reports/ and tmp/ under the data directory and
	// sweeps what an interrupted write left behind before anything is served.
	lib, err := reports.Open(reports.Options{
		DB: st.DB(), DataDir: cfg.DataDir, MaxUploadBytes: cfg.MaxUploadBytes, Now: time.Now,
	})
	if err != nil {
		log.Error("cannot prepare the report library", "error", err)
		return 1
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Error("cannot listen", "error", err)
		return 1
	}

	log.Info("listening", "addr", ln.Addr().String(), "base_url", cfg.BaseURL, "owner_github_id", cfg.OwnerGitHubID)
	if cfg.GitHubOverridden {
		log.Warn("the identity provider endpoints are overridden",
			"github_url", cfg.GitHubURL, "github_api_url", cfg.GitHubAPIURL)
	}

	srv := &http.Server{
		Handler:           server.New(cfg, st, lib, time.Now),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server stopped", "error", err)
			return 1
		}
		return 0
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "error", err)
	}
	return 0
}
