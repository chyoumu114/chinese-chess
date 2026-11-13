package api

// Cross-cutting HTTP middleware: security headers, rate limiting and the CORS
// allow-list policy. Kept apart from server.go so the routing table stays
// readable.

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"xiangqi/internal/config"
)

const (
	// aiSlots is how many engine searches may run at once. The WebSocket path
	// already budgets its own searches; this covers the HTTP entry point.
	aiSlots = 4
	// maxAIBudget caps the search time an HTTP caller may request.
	maxAIBudget = 800 * time.Millisecond
)

// limiters groups the per-route buckets. Rates are requests per minute.
type limiters struct {
	auth *Limiter // login and registration: brute-force surface
	ai   *Limiter // engine search: expensive per request
	read *Limiter // everything else under /api
}

func newLimiters(cfg config.Config) *limiters {
	if cfg.RateLimitDisabled {
		// A nil bucket allows everything; the handlers stay unchanged.
		return &limiters{}
	}
	// The auth bucket has to be tight enough to slow a password-guessing loop
	// yet loose enough for a busy morning: ten immediate attempts, then one
	// every three seconds. Per-account lockout is the proper defence against
	// brute force; this is the blanket under it.
	return &limiters{
		auth: NewLimiter(20, 10),
		ai:   NewLimiter(20, 4),
		read: NewLimiter(180, 60),
	}
}

// rateLimited wraps one handler with its own bucket.
func (s *Server) rateLimited(route string, l *Limiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			s.logger.Warn("rate limited", "route", route, "ip", clientIP(r))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后再试")
			return
		}
		next(w, r)
	}
}

// withRateLimit is the coarse baseline for every dynamic route. The per-route
// buckets layered on top stay sharp for the expensive endpoints.
func (s *Server) withRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets are served from disk and need no accounting.
		if !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/ws" {
			next.ServeHTTP(w, r)
			return
		}
		if !s.limits.read.Allow(clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			s.logger.Warn("rate limited", "route", "read", "ip", clientIP(r))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后再试")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withSecurityHeaders sets the headers a browser needs to lock the page down.
// HSTS is only added when the request arrived over TLS (directly or through the
// reverse proxy); over plain HTTP browsers ignore it anyway.
func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// The SPA loads its own assets and talks to its own WebSocket; inline
		// styles are required by Vue's dynamic style bindings.
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; "+
				"style-src 'self' 'unsafe-inline'; script-src 'self'; "+
				"connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'self'")
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed applies the CORS allow-list policy.
//
// An empty list means "same-origin only" outside development. Development
// accepts the localhost dev-server origins, because `npm run dev` serves the SPA
// from a different port than the API.
func (s *Server) originAllowed(origin string) bool {
	if containsFold(s.cfg.AllowedOrigins, origin) {
		return true
	}
	if len(s.cfg.AllowedOrigins) > 0 {
		return false
	}
	if s.cfg.IsProduction() {
		return false
	}
	return isLocalDevOrigin(origin)
}

// isLocalDevOrigin reports whether origin is a localhost dev server.
func isLocalDevOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}
