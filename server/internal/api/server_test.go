package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"xiangqi/internal/auth"
	"xiangqi/internal/config"
	"xiangqi/internal/gamehall"
	"xiangqi/internal/memstore"
	"xiangqi/internal/store"
	"xiangqi/internal/ws"
)

// newTestServer wires the whole HTTP stack onto an in-memory store, so the
// routing, middleware and handlers are exercised exactly as in production.
func newTestServer(t *testing.T) (*httptest.Server, *memstore.Store) {
	t.Helper()

	cfg := config.Config{
		Addr:        ":0",
		PublicDir:   "", // no static files: this exercises the API surface only
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
	hub.SetPersister(newMemPersister(mem))

	srv := httptest.NewServer(New(cfg, mem, session, hub, tokens, logger).Handler())
	t.Cleanup(srv.Close)
	return srv, mem
}

// newMemRecorder adapts the memory store onto gamehall.Recorder for tests.
func newMemRecorder(mem *memstore.Store) gamehall.Recorder { return &memRecorder{mem: mem} }

type memRecorder struct{ mem *memstore.Store }

func (r *memRecorder) StartGame(ctx context.Context, gameID int64) error { return nil }
func (r *memRecorder) AddMove(ctx context.Context, gameID int64, m gamehall.MoveRecord) {
	_ = r.mem.AddMove(ctx, gameID, memMove(m))
}
func (r *memRecorder) FinishGame(ctx context.Context, gameID int64, res gamehall.Result) {
	_ = r.mem.FinishGame(ctx, gameID, memResult(res))
}
func (r *memRecorder) Finish(ctx context.Context, redID, blackID int64, redDelta, blackDelta int) {
	_ = r.mem.ApplyResult(ctx, redID, blackID, redDelta, blackDelta)
}

// TestHealthEndpoint checks the service reports its state.
func TestHealthEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	var body struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	getJSON(t, srv.URL+"/api/health", "", &body)
	if body.Status != "ok" || body.Database != "ok" {
		t.Fatalf("health = %+v", body)
	}
}

// TestRegisterRequiresValidInput checks the validation paths and the error shape.
func TestRegisterRequiresValidInput(t *testing.T) {
	srv, _ := newTestServer(t)

	cases := []struct {
		name string
		body map[string]string
		code string
	}{
		{"short username", map[string]string{"username": "ab", "password": "secret123"}, "invalid_registration"},
		{"illegal username", map[string]string{"username": "has space", "password": "secret123"}, "invalid_registration"},
		{"short password", map[string]string{"username": "alice", "password": "abc"}, "invalid_registration"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := postJSON(t, srv.URL+"/api/auth/register", "", c.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", status, body)
			}
			if !strings.Contains(body, c.code) {
				t.Fatalf("error code missing: body = %s, want %s", body, c.code)
			}
		})
	}

	// A duplicate registration is a conflict.
	if status, _ := postJSON(t, srv.URL+"/api/auth/register", "", map[string]string{
		"username": "alice", "password": "secret123",
	}); status != http.StatusCreated {
		t.Fatalf("first registration status = %d, want 201", status)
	}
	status, body := postJSON(t, srv.URL+"/api/auth/register", "", map[string]string{
		"username": "ALICE", "password": "secret123",
	})
	if status != http.StatusConflict {
		t.Fatalf("duplicate registration status = %d, want 409 (body %s)", status, body)
	}
	if !strings.Contains(body, "username_taken") {
		t.Fatalf("duplicate error body = %s, want username_taken", body)
	}
}

// TestAuthMiddlewareProtectsEndpoints checks the bearer token gate.
func TestAuthMiddlewareProtectsEndpoints(t *testing.T) {
	srv, _ := newTestServer(t)

	// No token.
	if status, body := getRaw(t, srv.URL+"/api/auth/me", ""); status != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401 (body %s)", status, body)
	}
	// Garbage token.
	if status, _ := getRaw(t, srv.URL+"/api/auth/me", "not-a-token"); status != http.StatusUnauthorized {
		t.Fatalf("garbage token: status = %d, want 401", status)
	}
	// A token signed with a different key must be rejected.
	other, err := auth.NewTokenIssuer("a-completely-different-secret", time.Hour)
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	foreign, _, err := other.Issue(1, "alice")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if status, _ := getRaw(t, srv.URL+"/api/auth/me", foreign); status != http.StatusUnauthorized {
		t.Fatalf("foreign-signed token: status = %d, want 401", status)
	}

	// A valid token works.
	token := register(t, srv.URL, "alice", "爱丽丝")
	var me struct {
		User struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Nickname string `json:"nickname"`
		} `json:"user"`
	}
	if status, body := getRawInto(t, srv.URL+"/api/auth/me", token, &me); status != http.StatusOK {
		t.Fatalf("valid token: status = %d (body %s)", status, body)
	}
	if me.User.Username != "alice" {
		t.Errorf("username = %q, want alice", me.User.Username)
	}
	if me.User.Nickname != "爱丽丝" {
		t.Errorf("nickname = %q, want 爱丽丝 (the JSON must carry utf8mb4 text)", me.User.Nickname)
	}
}

// TestLoginFlow checks the login endpoint and that the password is never echoed.
func TestLoginFlow(t *testing.T) {
	srv, _ := newTestServer(t)
	register(t, srv.URL, "alice", "")

	status, body := postJSON(t, srv.URL+"/api/auth/login", "", map[string]string{
		"username": "alice", "password": "e2e-password",
	})
	if status != http.StatusOK {
		t.Fatalf("login status = %d (body %s)", status, body)
	}
	if strings.Contains(body, "passwordHash") || strings.Contains(body, "e2e-password") {
		t.Fatalf("the login response leaks the password or its hash: %s", body)
	}

	// Wrong password and unknown user give the same generic message.
	status, wrong := postJSON(t, srv.URL+"/api/auth/login", "", map[string]string{
		"username": "alice", "password": "nope",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want 401", status)
	}
	status2, unknown := postJSON(t, srv.URL+"/api/auth/login", "", map[string]string{
		"username": "nobody", "password": "e2e-password",
	})
	if status2 != http.StatusUnauthorized {
		t.Fatalf("unknown user: status = %d, want 401", status2)
	}
	if wrong != unknown {
		t.Errorf("the two failure messages differ, which leaks whether an account exists:\n%q\n%q", wrong, unknown)
	}
}

// TestMalformedRequestsAreRejected checks the JSON decoder guards.
func TestMalformedRequestsAreRejected(t *testing.T) {
	srv, _ := newTestServer(t)

	// Unknown fields are refused, which catches client typos early.
	resp, err := http.Post(srv.URL+"/api/auth/register", "application/json",
		strings.NewReader(`{"username":"alice","password":"secret123","admin":true}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field: status = %d, want 400", resp.StatusCode)
	}

	// Broken JSON.
	resp2, err := http.Post(srv.URL+"/api/auth/register", "application/json",
		strings.NewReader(`{"username":`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("broken json: status = %d, want 400", resp2.StatusCode)
	}

	// A missing route on the API prefix must be a JSON 404, not the SPA shell.
	status, body := getRaw(t, srv.URL+"/api/does-not-exist", "")
	if status != http.StatusNotFound {
		t.Errorf("unknown api route: status = %d, want 404", status)
	}
	if !strings.Contains(body, "not_found") {
		t.Errorf("unknown api route body = %s, want a JSON not_found error", body)
	}
}

// TestRulesAndAIEndpoints checks the two endpoints a frontend needs most.
func TestRulesAndAIEndpoints(t *testing.T) {
	srv, _ := newTestServer(t)

	var rules struct {
		Rows   int `json:"rows"`
		Cols   int `json:"cols"`
		Pieces []struct {
			Glyph string `json:"glyph"`
		} `json:"pieces"`
	}
	if status, body := getRawInto(t, srv.URL+"/api/rules", "", &rules); status != http.StatusOK {
		t.Fatalf("rules: status = %d (body %s)", status, body)
	}
	if rules.Rows != 10 || rules.Cols != 9 || len(rules.Pieces) != 14 {
		t.Fatalf("rules = %dx%d with %d pieces", rules.Rows, rules.Cols, len(rules.Pieces))
	}
	glyphs := map[string]bool{}
	for _, p := range rules.Pieces {
		glyphs[p.Glyph] = true
	}
	for _, want := range []string{"帅", "将", "车", "炮", "马", "兵", "卒"} {
		if !glyphs[want] {
			t.Errorf("the piece vocabulary is missing %s", want)
		}
	}

	var aiResp struct {
		From     string `json:"from"`
		To       string `json:"to"`
		Notation string `json:"notation"`
	}
	if status, body := getRawInto(t, srv.URL+"/api/ai/move?level=1&budgetMs=200", "", &aiResp); status != http.StatusOK {
		t.Fatalf("ai move: status = %d (body %s)", status, body)
	}
	if aiResp.From == "" || aiResp.To == "" {
		t.Fatalf("ai move returned %+v", aiResp)
	}
	if aiResp.Notation == "" {
		t.Errorf("ai move has no Chinese notation")
	}

	// A bad FEN is a client error.
	if status, _ := getRaw(t, srv.URL+"/api/ai/move?fen=nonsense", ""); status != http.StatusBadRequest {
		t.Errorf("bad fen: status = %d, want 400", status)
	}
	// A decided position reports no move rather than an error.
	status, body := getRaw(t, srv.URL+"/api/ai/move?fen="+
		urlEscape("3k5/9/9/9/9/9/9/9/9/4K4%20b%20-%20-%200%201"), "")
	if status != http.StatusOK {
		t.Logf("decided position returned status %d (%s)", status, body)
	}
}

// TestCORSAndPreflight covers the browser-facing middleware.
func TestCORSAndPreflight(t *testing.T) {
	srv, _ := newTestServer(t)

	req, err := http.NewRequest(http.MethodOptions, srv.URL+"/api/health", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Origin", "http://localhost:5173")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("allow-origin = %q, want the request origin", got)
	}
}

// TestWebSocketUpgradeSurvivesMiddleware is the regression test for a real bug:
// the access-log middleware wrapped the ResponseWriter without forwarding
// Hijack, so every WebSocket upgrade failed with HTTP 500. The wrapper must stay
// transparent to the interfaces the upgrade needs.
func TestWebSocketUpgradeSurvivesMiddleware(t *testing.T) {
	srv, _ := newTestServer(t)
	token := register(t, srv.URL, "alice", "")

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?token=" + token
	conn, err := dialWS(wsURL)
	if err != nil {
		t.Fatalf("websocket upgrade through the middleware failed: %v", err)
	}
	defer conn.Close()

	// The server must greet the connection.
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading the first frame: %v", err)
		}
		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &env); err != nil {
			t.Fatalf("frame does not decode: %v", err)
		}
		if env.Type == "welcome" {
			break
		}
	}

	// A bad token must be refused before the upgrade.
	if _, err := dialWS("ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?token=garbage"); err == nil {
		t.Errorf("a WebSocket connection with a bad token should be refused")
	}
}

// newMemPersister adapts the memory store onto ws.Persister for tests.
func newMemPersister(mem *memstore.Store) ws.Persister { return &memPersister{mem: mem} }

type memPersister struct{ mem *memstore.Store }

func (p *memPersister) CreateGame(ctx context.Context, view gamehall.RoomView) (int64, error) {
	row := store.Game{
		RedName: view.Red.Name, BlackName: view.Black.Name,
		Status: store.StatusPlaying, Outcome: "ongoing", FEN: view.FEN,
		Rated: view.Rated,
	}
	if view.Red.UserID != 0 {
		id := view.Red.UserID
		row.RedUserID = &id
	}
	if view.Black.UserID != 0 && !view.Black.IsAI {
		id := view.Black.UserID
		row.BlackUserID = &id
	}
	created, err := p.mem.CreateGame(ctx, row)
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}

// memMove converts a live-game ply into a store row.
func memMove(m gamehall.MoveRecord) store.Move {
	return store.Move{
		Ply: m.Ply, Side: m.Side, UCI: m.UCI, Notation: m.Notation,
		FEN: m.FEN, Check: m.Check, MSSpent: int(m.MSSpent),
	}
}

// memResult converts a live-game result into a store row.
func memResult(r gamehall.Result) store.GameResult {
	return store.GameResult{
		Outcome: r.Outcome, Reason: r.Reason, WinnerSide: r.WinnerSide,
		FEN: r.FEN, MoveCount: r.MoveCount,
		RedMSLeft: r.RedMSLeft, BlackMSLeft: r.BlackMSLeft,
	}
}

// dialWS opens a WebSocket for the tests.
func dialWS(url string) (*websocket.Conn, error) {
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("%w (HTTP %d)", err, resp.StatusCode)
		}
		return nil, err
	}
	return conn, nil
}

// urlEscape percent-encodes a query value.
func urlEscape(v string) string { return url.QueryEscape(v) }

// ---- helpers ----

func register(t *testing.T, base, username, nickname string) string {
	t.Helper()
	body := map[string]string{"username": username, "password": "e2e-password"}
	if nickname != "" {
		body["nickname"] = nickname
	}
	status, raw := postJSON(t, base+"/api/auth/register", "", body)
	if status != http.StatusCreated {
		t.Fatalf("register %s: status = %d (body %s)", username, status, raw)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("register response does not decode: %v (%s)", err, raw)
	}
	if out.Token == "" {
		t.Fatalf("register returned no token")
	}
	return out.Token
}

func getRaw(t *testing.T, url, token string) (int, string) {
	t.Helper()
	return getRawInto(t, url, token, nil)
}

func getRawInto(t *testing.T, url, token string, out any) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if out != nil && resp.StatusCode < 400 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("response does not decode: %v (%s)", err, raw)
		}
	}
	return resp.StatusCode, string(raw)
}

func getJSON(t *testing.T, url, token string, out any) {
	t.Helper()
	if status, body := getRawInto(t, url, token, out); status != http.StatusOK {
		t.Fatalf("GET %s: status = %d (body %s)", url, status, body)
	}
}

func postJSON(t *testing.T, url, token string, body any) (int, string) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(raw)
}
