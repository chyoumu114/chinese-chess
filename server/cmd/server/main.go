// Command server runs the xiangqi HTTP and WebSocket service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"xiangqi/internal/api"
	"xiangqi/internal/auth"
	"xiangqi/internal/config"
	"xiangqi/internal/gamehall"
	"xiangqi/internal/memstore"
	"xiangqi/internal/store"
	"xiangqi/internal/ws"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		migrateOnly = flag.Bool("migrate", false, "apply the database schema and exit")
		noCreateDB  = flag.Bool("no-create-db", false, "do not try to create the database if it is missing")
		storeKind   = flag.String("store", "mysql", "persistence backend: mysql or memory")
		logLevel    = flag.String("log-level", "", "log level: debug, info, warn, error")
	)
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg, *logLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Choose the persistence backend. The in-memory one exists so the full
	// server can be run and tested without MySQL; it loses everything on exit.
	var (
		dataStore api.DataStore
		gameStore GameStore
		closer    func() error
	)
	switch strings.ToLower(strings.TrimSpace(*storeKind)) {
	case "memory", "mem", "inmemory":
		mem := memstore.New()
		dataStore, gameStore, closer = mem, mem, nil
		logger.Warn("using the in-memory store: all accounts and games are lost on exit")
	case "mysql", "":
		st, err := store.Open(ctx, cfg.DB, !*noCreateDB)
		if err != nil {
			return err
		}
		defer st.Close()
		if err := st.Migrate(ctx); err != nil {
			return err
		}
		logger.Info("database ready", "host", cfg.DB.Host, "port", cfg.DB.Port, "schema", cfg.DB.Name)
		if *migrateOnly {
			return nil
		}
		dataStore, gameStore, closer = st, st, st.Close
	default:
		return fmt.Errorf("unknown -store %q: use mysql or memory", *storeKind)
	}
	_ = closer

	tokens, err := auth.NewTokenIssuer(cfg.JWTSecret, cfg.TokenTTL)
	if err != nil {
		return err
	}

	recorder := newStoreRecorder(gameStore)
	session := gamehall.NewSession(gamehall.Config{
		TurnTimeout: cfg.TurnTimeout,
		GameTimeout: cfg.GameTimeout,
	}, recorder)

	hub := ws.NewHub(session, logger, cfg.AllowedOrigins)
	hub.SetPersister(newStorePersister(gameStore))

	apiServer := api.New(cfg, dataStore, session, hub, tokens, logger)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: a WebSocket connection is long lived, and a write
		// deadline would cut it off. Per-write deadlines are set inside the hub.
		IdleTimeout: 120 * time.Second,
	}

	// Background jobs: clock expiry, presence and room reaping.
	go hub.StartTimers(ctx)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr, "env", cfg.Env, "staticDir", cfg.PublicDir)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hub.Close()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// newLogger builds the process logger.
func newLogger(cfg config.Config, override string) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(override)) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	case "info":
		level = slog.LevelInfo
	case "":
		if !cfg.IsProduction() {
			level = slog.LevelDebug
		}
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.IsProduction() {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
