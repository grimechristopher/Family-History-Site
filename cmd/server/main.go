// Command server runs the family history site.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grimechristopher/family-history-site/internal/config"
	"github.com/grimechristopher/family-history-site/internal/migrate"
	"github.com/grimechristopher/family-history-site/internal/store"
	"github.com/grimechristopher/family-history-site/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := pool.Ping(startup); err != nil {
		return err
	}
	// Migrations run at boot, so deployment is just "start the new binary".
	if err := migrate.Run(startup, pool); err != nil {
		return err
	}
	log.Info("schema up to date")

	s := store.New(pool)

	// Static assets are cached hard, so they are versioned by process start time.
	assetVersion := strconv.FormatInt(time.Now().Unix(), 36)

	srv, err := web.New(cfg, s, log, assetVersion)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// Generous: someone may sit on a card for a long while before saving.
		ReadTimeout:  2 * time.Minute,
		WriteTimeout: 2 * time.Minute,
		IdleTimeout:  2 * time.Minute,
	}

	// Expired sessions are swept periodically rather than on every request.
	go sweepSessions(ctx, s, log)

	errs := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "base_url", cfg.BaseURL)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	}
}

func sweepSessions(ctx context.Context, s *store.Store, log *slog.Logger) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.DeleteExpiredSessions(ctx)
			if err != nil {
				log.Warn("could not sweep sessions", "err", err)
				continue
			}
			if n > 0 {
				log.Info("swept expired sessions", "count", n)
			}
		}
	}
}
