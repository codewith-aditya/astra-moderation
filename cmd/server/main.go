// Command server runs the astra-moderation HTTP API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/codewith-aditya/astra-moderation/internal/api"
	"github.com/codewith-aditya/astra-moderation/internal/config"
	"github.com/codewith-aditya/astra-moderation/internal/policy"
	"github.com/codewith-aditya/astra-moderation/internal/store"
	"github.com/codewith-aditya/astra-moderation/internal/upstream"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level()}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}

	pol, err := policy.Load(cfg.PolicyPath)
	if err != nil {
		logger.Warn("policy file not usable, falling back to built-in defaults", "path", cfg.PolicyPath, "err", err)
		pol = policy.Default()
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		logger.Error("store", "err", err)
		os.Exit(1)
	}

	up := upstream.New(cfg.UpstreamURL, cfg.UpstreamModel, cfg.UpstreamKey, cfg.DisableThinking, cfg.UpstreamTimeout)
	srv := api.New(cfg, pol, st, up, logger)

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// janitor: rate-limit windows + job TTL
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			srv.Limiter().GC()
			srv.Jobs().GC()
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("astra-moderation listening",
			"addr", cfg.Addr,
			"upstream", cfg.UpstreamURL,
			"model", cfg.UpstreamModel,
			"master_key_file", cfg.DataDir+"/master.key",
			"version", api.Version,
		)
		errCh <- httpSrv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server", "err", err)
			os.Exit(1)
		}
		return
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
	logger.Info("bye")
}

func level() slog.Level {
	switch os.Getenv("MOD_LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
