package api

// Token-bucket rate limiting for the public HTTP surface.
//
// Two properties matter here:
//
//   - A cheap request must not become an expensive one. /api/ai/move runs an
//     engine search, so its bucket is far stricter than the read-only routes.
//   - The bucket map must not grow without bound. An attacker rotating source
//     addresses would otherwise turn the limiter itself into a memory leak, so
//     idle buckets are swept periodically.

import (
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// bucket is a single key's token state. tokens is refilled lazily on access.
type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter is a token bucket keyed by an arbitrary string, usually the client
// address. The zero value is unusable; build one with NewLimiter.
type Limiter struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	rate      float64 // tokens added per second
	burst     float64 // bucket capacity
	idleTTL   time.Duration
	now       func() time.Time // injectable for tests
	lastSweep time.Time
}

// NewLimiter returns a limiter allowing perMinute requests sustained, with room
// for burst requests back to back.
func NewLimiter(perMinute, burst float64) *Limiter {
	return &Limiter{
		buckets: make(map[string]*bucket),
		rate:    perMinute / 60,
		burst:   burst,
		idleTTL: time.Hour,
		now:     time.Now,
	}
}

// Allow reports whether key may proceed, consuming one token when it may.
//
// A nil limiter allows everything: it keeps handler wiring simple in tests.
func (l *Limiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweepLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweepLocked forgets buckets no one has used for idleTTL. It runs at most
// every ten minutes so the bookkeeping stays off the hot path.
func (l *Limiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < 10*time.Minute {
		return
	}
	l.lastSweep = now
	for key, b := range l.buckets {
		if now.Sub(b.last) > l.idleTTL {
			delete(l.buckets, key)
		}
	}
}

// clientIP returns the address a request should be attributed to.
//
// X-Real-IP is only honoured when the request came from the loopback address,
// which is the reverse proxy in every supported deployment (the server binds
// 127.0.0.1). Trusting the header unconditionally would let any caller spoof
// it and slide past the limiter.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if isLoopbackHost(host) {
		if forwarded := strings.TrimSpace(r.Header.Get("X-Real-IP")); forwarded != "" {
			if ip := net.ParseIP(forwarded); ip != nil {
				return ip.String()
			}
		}
	}
	return host
}

// isLoopbackHost reports whether host names the local machine.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
