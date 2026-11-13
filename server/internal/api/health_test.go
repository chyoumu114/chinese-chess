package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xiangqi/internal/auth"
	"xiangqi/internal/config"
	"xiangqi/internal/gamehall"
	"xiangqi/internal/memstore"
	"xiangqi/internal/ws"
)

// failingPingStore overrides only the health check, so the test can describe a
// database failure without implementing the whole DataStore surface.
type failingPingStore struct {
	DataStore
	err error
}

func (f failingPingStore) Ping(context.Context) error { return f.err }

// TestHealthHidesInternalErrors: /api/health is unauthenticated, so the driver
// text and the internal address must stay in the log, not in the response.
func TestHealthHidesInternalErrors(t *testing.T) {
	cfg := config.Config{
		Addr:        ":0",
		JWTSecret:   "test-secret-0123456789abcdef",
		TokenTTL:    time.Hour,
		TurnTimeout: time.Minute,
		GameTimeout: 10 * time.Minute,
		Env:         "development",
	}
	mem := memstore.New()
	tokens, err := auth.NewTokenIssuer(cfg.JWTSecret, cfg.TokenTTL)
	if err != nil {
		t.Fatalf("token issuer: %v", err)
	}
	session := gamehall.NewSession(gamehall.Config{
		TurnTimeout: cfg.TurnTimeout,
		GameTimeout: cfg.GameTimeout,
	}, newMemRecorder(mem))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := ws.NewHub(session, logger, cfg.AllowedOrigins)

	failing := failingPingStore{
		DataStore: mem,
		err:       errors.New("dial tcp 10.0.0.5:3306: connect: connection refused"),
	}
	srv := httptest.NewServer(New(cfg, failing, session, hub, tokens, logger).Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	text := string(body)
	if strings.Contains(text, "10.0.0.5") || strings.Contains(text, "dial tcp") {
		t.Fatalf("health response leaked internal detail: %s", text)
	}
	if !strings.Contains(text, "unavailable") {
		t.Fatalf("expected a neutral database status, got: %s", text)
	}
	if !strings.Contains(text, "degraded") {
		t.Fatalf("overall status should report degraded, got: %s", text)
	}
}
