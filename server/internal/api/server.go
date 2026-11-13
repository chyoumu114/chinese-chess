// Package api exposes the REST surface: accounts, lobby, saved games and the
// WebSocket upgrade. It owns the shared server state and the middleware.
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xiangqi/internal/auth"
	"xiangqi/internal/config"
	"xiangqi/internal/gamehall"
	"xiangqi/internal/store"
	"xiangqi/internal/ws"
)

// DataStore is the persistence surface the HTTP layer needs. It is an interface
// so the API can be exercised against an in-memory implementation, which keeps
// the whole request path testable without a database; *store.Store satisfies it.
type DataStore interface {
	UserByID(ctx context.Context, id int64) (store.User, error)
	CreateUser(ctx context.Context, username, nickname, password string) (store.User, error)
	Authenticate(ctx context.Context, username, password string) (store.User, error)
	Leaderboard(ctx context.Context, limit int) ([]store.User, error)
	StatsForUser(ctx context.Context, userID int64) (store.UserStats, error)
	ListGames(ctx context.Context, f store.GameFilter) ([]store.Game, error)
	GameByID(ctx context.Context, id int64) (store.Game, error)
	Moves(ctx context.Context, gameID int64) ([]store.Move, error)
	// Ping reports whether the backing store is reachable.
	Ping(ctx context.Context) error
}

// Server bundles the dependencies every handler needs.
type Server struct {
	cfg     config.Config
	store   DataStore
	session *gamehall.Session
	hub     *ws.Hub
	tokens  *auth.TokenIssuer
	logger  *slog.Logger

	// limits holds the per-route token buckets; aiSem caps concurrent engine
	// searches so a burst of requests cannot pin every core.
	limits *limiters
	aiSem  chan struct{}

	mux *http.ServeMux
}

// New builds the HTTP server.
func New(cfg config.Config, st DataStore, session *gamehall.Session, hub *ws.Hub, tokens *auth.TokenIssuer, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		cfg:     cfg,
		store:   st,
		session: session,
		hub:     hub,
		tokens:  tokens,
		logger:  logger,
		limits:  newLimiters(cfg),
		aiSem:   make(chan struct{}, aiSlots),
		mux:     http.NewServeMux(),
	}
	s.routes()
	return s
}

// Handler returns the composed handler, including static files and middleware.
func (s *Server) Handler() http.Handler {
	return s.withRecovery(s.withSecurityHeaders(s.withRateLimit(s.withLogging(s.withCORS(s.mux)))))
}

// routes registers every endpoint. Go's pattern syntax (method and wildcards)
// keeps the router dependency free.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.handleHealth)

	// Credential endpoints get their own tight bucket: they are the
	// brute-force surface, and each attempt costs a password hash.
	s.mux.HandleFunc("POST /api/auth/register", s.rateLimited("register", s.limits.auth, s.handleRegister))
	s.mux.HandleFunc("POST /api/auth/login", s.rateLimited("login", s.limits.auth, s.handleLogin))
	s.mux.HandleFunc("GET /api/auth/me", s.auth(s.handleMe))

	s.mux.HandleFunc("GET /api/leaderboard", s.handleLeaderboard)
	s.mux.HandleFunc("GET /api/lobby", s.handleLobby)
	s.mux.HandleFunc("GET /api/users/{id}", s.handleUserProfile)

	s.mux.HandleFunc("GET /api/games", s.handleListGames)
	s.mux.HandleFunc("GET /api/games/{id}", s.handleGetGame)
	s.mux.HandleFunc("GET /api/games/{id}/moves", s.handleGetGameMoves)

	s.mux.HandleFunc("GET /api/rules", s.handleRules)
	// The engine search is the only endpoint whose cost is decided by the
	// caller, so it also gets its own bucket on top of the global one.
	s.mux.HandleFunc("GET /api/ai/move", s.rateLimited("ai", s.limits.ai, s.handleAIMove))

	s.mux.HandleFunc("GET /ws", s.handleWebSocket)

	// Anything else under /api/ must still answer with the JSON error shape.
	// Without this the ServeMux writes its own plain-text "404 page not found",
	// which a client cannot parse.
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "接口不存在")
	})

	s.mountStatic()
}

// mountStatic serves the built single-page application when it is present.
func (s *Server) mountStatic() {
	if s.cfg.PublicDir == "" {
		return
	}
	dir := http.Dir(s.cfg.PublicDir)
	fileServer := http.FileServer(dir)

	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" {
			writeError(w, http.StatusNotFound, "not_found", "接口不存在")
			return
		}
		// A client-side route such as /lobby has no file on disk; serve the
		// application shell so the router can handle it.
		if r.URL.Path != "/" && !hasFile(dir, r.URL.Path) {
			serveIndex(w, r, dir)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// hasFile reports whether a path resolves to a regular file under dir.
func hasFile(dir http.Dir, urlPath string) bool {
	f, err := dir.Open(urlPath)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	return err == nil && !info.IsDir()
}

// serveIndex writes the single-page application shell.
func serveIndex(w http.ResponseWriter, r *http.Request, dir http.Dir) {
	f, err := dir.Open("/index.html")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "页面不存在")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stat_failed", err.Error())
		return
	}
	http.ServeContent(w, r, "index.html", info.ModTime(), f)
}

// ---- middleware ----

// statusRecorder captures the response code for the access log.
//
// It must stay transparent to the interfaces the WebSocket upgrade needs:
// gorilla/websocket type-asserts the writer to http.Hijacker, so wrapping
// without forwarding Hijack turns every upgrade into HTTP 500. Flush is
// forwarded for the same reason, to keep streaming responses working.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Hijack takes over the connection for the WebSocket handshake.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("api: the response writer does not support hijacking")
	}
	// A hijacked connection never produces a normal response, so record that it
	// was taken over for logging purposes.
	if r.status == 0 {
		r.status = http.StatusSwitchingProtocols
	}
	return hijacker.Hijack()
}

// Flush forwards to the underlying writer when it supports flushing.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		// The WebSocket handler streams for the connection's lifetime, so its
		// duration is not a useful access-log signal.
		if strings.HasPrefix(r.URL.Path, "/ws") {
			return
		}
		level := slog.LevelInfo
		if rec.status >= 500 {
			level = slog.LevelError
		} else if rec.status >= 400 {
			level = slog.LevelWarn
		}
		s.logger.Log(r.Context(), level, "http",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"bytes", rec.bytes, "took", time.Since(start).String())
	})
}

func (s *Server) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.logger.Error("panic serving request", "path", r.URL.Path, "panic", v)
				writeError(w, http.StatusInternalServerError, "internal", "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			// No Allow-Credentials: the session travels in the Authorization
			// header, and that flag only widens the attack surface.
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSuffix(v, "/"), strings.TrimSuffix(want, "/")) {
			return true
		}
	}
	return false
}

// ---- request context ----

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxClaims
)

// currentUser returns the authenticated account, when there is one.
func currentUser(r *http.Request) (store.User, bool) {
	u, ok := r.Context().Value(ctxUser).(store.User)
	return u, ok
}

// currentClaims returns the verified token claims, when there are any.
func currentClaims(r *http.Request) (auth.Claims, bool) {
	c, ok := r.Context().Value(ctxClaims).(auth.Claims)
	return c, ok
}

// auth wraps a handler so that it requires a valid bearer token.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := auth.ParseBearer(r.Header.Get("Authorization"))
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing_token", "请先登录")
			return
		}
		claims, err := s.tokens.Parse(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "bad_token", "登录状态已失效，请重新登录")
			return
		}
		user, err := s.store.UserByID(r.Context(), claims.Subject)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unknown_user", "账号不存在")
			return
		}
		ctx := context.WithValue(r.Context(), ctxUser, user)
		ctx = context.WithValue(ctx, ctxClaims, claims)
		next(w, r.WithContext(ctx))
	}
}

// optionalAuth attaches the caller when a token is present but never rejects.
func (s *Server) optionalAuth(r *http.Request) *http.Request {
	token, ok := auth.ParseBearer(r.Header.Get("Authorization"))
	if !ok {
		return r
	}
	claims, err := s.tokens.Parse(token)
	if err != nil {
		return r
	}
	user, err := s.store.UserByID(r.Context(), claims.Subject)
	if err != nil {
		return r
	}
	ctx := context.WithValue(r.Context(), ctxUser, user)
	ctx = context.WithValue(ctx, ctxClaims, claims)
	return r.WithContext(ctx)
}

// ---- response helpers ----

// writeJSON writes v as JSON with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// The header is already committed, so the only option is to log.
		slog.Default().Error("writing json response failed", "error", err)
	}
}

// errorBody is the shape of every error response.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}

// decodeJSON reads a JSON body into v, rejecting unknown fields and oversized
// payloads.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "请求格式不正确")
		return false
	}
	return true
}

// pathID reads a numeric path parameter.
func pathID(r *http.Request, name string) (int64, bool) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// queryInt reads an integer query parameter with a default.
func queryInt(r *http.Request, name string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}
