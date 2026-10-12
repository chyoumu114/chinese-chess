package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xiangqi/internal/auth"
	"xiangqi/internal/config"
	"xiangqi/internal/gamehall"
	"xiangqi/internal/memstore"
	"xiangqi/internal/ws"
)

// newStaticServer builds a server whose public directory holds a minimal
// single-page application.
func newStaticServer(t *testing.T) *httptest.Server {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"),
		[]byte(`<!doctype html><html><body><div id="app">象棋</div></body></html>`), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"),
		[]byte("console.log('xiangqi');"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	cfg := config.Config{
		PublicDir:   dir,
		JWTSecret:   "test-secret-0123456789abcdef",
		TokenTTL:    time.Hour,
		TurnTimeout: time.Minute,
		GameTimeout: time.Minute,
		Env:         "development",
	}
	mem := memstore.New()
	tokens, err := auth.NewTokenIssuer(cfg.JWTSecret, cfg.TokenTTL)
	if err != nil {
		t.Fatalf("token issuer: %v", err)
	}
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := ws.NewHub(session, logger, nil)

	srv := httptest.NewServer(New(cfg, mem, session, hub, tokens, logger).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestStaticFilesAreServed checks the built frontend is reachable.
func TestStaticFilesAreServed(t *testing.T) {
	srv := newStaticServer(t)

	// The root serves the application shell.
	status, body := getRaw(t, srv.URL+"/", "")
	if status != http.StatusOK {
		t.Fatalf("GET /: status = %d", status)
	}
	if !strings.Contains(body, `id="app"`) || !strings.Contains(body, "象棋") {
		t.Fatalf("GET / did not return the shell: %s", body)
	}

	// A real asset is served as-is.
	status, body = getRaw(t, srv.URL+"/assets/app.js", "")
	if status != http.StatusOK {
		t.Fatalf("GET /assets/app.js: status = %d", status)
	}
	if !strings.Contains(body, "xiangqi") {
		t.Fatalf("the asset content is wrong: %s", body)
	}
}

// TestClientSideRoutesFallBackToTheShell checks that a path with no file on disk
// returns index.html, so the Vue router can handle a deep link or a refresh.
func TestClientSideRoutesFallBackToTheShell(t *testing.T) {
	srv := newStaticServer(t)

	for _, route := range []string{"/lobby", "/game/1", "/history", "/leaderboard", "/profile"} {
		status, body := getRaw(t, srv.URL+route, "")
		if status != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200 from the SPA shell", route, status)
			continue
		}
		if !strings.Contains(body, `id="app"`) {
			t.Errorf("GET %s did not return the shell: %s", route, body)
		}
	}
}

// TestAPIWinsOverStaticFiles checks a deep API path is never answered with HTML,
// even though the SPA fallback would otherwise match it.
func TestAPIWinsOverStaticFiles(t *testing.T) {
	srv := newStaticServer(t)

	status, body := getRaw(t, srv.URL+"/api/nope/deeper", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if strings.Contains(body, "<html") || strings.Contains(body, `id="app"`) {
		t.Fatalf("an API path was answered with the SPA shell: %s", body)
	}
	if !strings.Contains(body, "not_found") {
		t.Fatalf("body = %s, want a JSON not_found error", body)
	}

	// The health endpoint must still work with static hosting enabled.
	if status, _ := getRaw(t, srv.URL+"/api/health", ""); status != http.StatusOK {
		t.Errorf("GET /api/health: status = %d, want 200", status)
	}
}

// TestStaticDirMissingIsNotFatal checks the server runs without a built
// frontend, which is how the API is exercised on its own.
func TestStaticDirMissingIsNotFatal(t *testing.T) {
	cfg := config.Config{
		PublicDir:   filepath.Join(t.TempDir(), "does-not-exist"),
		JWTSecret:   "test-secret-0123456789abcdef",
		TokenTTL:    time.Hour,
		TurnTimeout: time.Minute,
		Env:         "development",
	}
	mem := memstore.New()
	tokens, err := auth.NewTokenIssuer(cfg.JWTSecret, cfg.TokenTTL)
	if err != nil {
		t.Fatalf("token issuer: %v", err)
	}
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := ws.NewHub(session, logger, nil)
	srv := httptest.NewServer(New(cfg, mem, session, hub, tokens, logger).Handler())
	t.Cleanup(srv.Close)

	if status, _ := getRaw(t, srv.URL+"/api/health", ""); status != http.StatusOK {
		t.Errorf("health with no static dir: status = %d, want 200", status)
	}
	if status, _ := getRaw(t, srv.URL+"/", ""); status == http.StatusOK {
		t.Errorf("GET / with no static dir returned 200; there is nothing to serve")
	}
}
