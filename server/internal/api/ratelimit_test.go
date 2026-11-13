package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLimiterAllowsBurstThenRefills pins the token-bucket contract: a burst is
// allowed, the next call is refused, and time restores one token.
func TestLimiterAllowsBurstThenRefills(t *testing.T) {
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewLimiter(60, 3) // one token per second, capacity three
	l.now = func() time.Time { return clock }

	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("burst request %d should be allowed", i+1)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("the fourth request must be refused")
	}

	clock = clock.Add(time.Second)
	if !l.Allow("1.2.3.4") {
		t.Fatal("one second should refill exactly one token")
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("only one token was refilled")
	}
}

// TestLimiterKeysAreIndependent makes sure one noisy caller cannot drain
// everyone else's budget.
func TestLimiterKeysAreIndependent(t *testing.T) {
	l := NewLimiter(60, 1)

	if !l.Allow("a") {
		t.Fatal("a should pass")
	}
	if l.Allow("a") {
		t.Fatal("a should be drained")
	}
	if !l.Allow("b") {
		t.Fatal("b has its own bucket")
	}
}

// TestLimiterSweepsIdleBuckets guards against unbounded growth when the caller
// rotates source addresses: forgetting the sweep turns the limiter itself into
// a memory leak.
func TestLimiterSweepsIdleBuckets(t *testing.T) {
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewLimiter(60, 1)
	l.now = func() time.Time { return clock }

	l.Allow("old")
	clock = clock.Add(2 * time.Hour)
	l.Allow("fresh") // this call triggers the sweep

	if _, ok := l.buckets["old"]; ok {
		t.Fatal("idle bucket should have been swept")
	}
	if _, ok := l.buckets["fresh"]; !ok {
		t.Fatal("the active bucket must survive")
	}
}

// TestClientIPTrustsRealIPOnlyFromLoopback covers the spoofing boundary:
// X-Real-IP is authoritative behind our own reverse proxy (loopback) and
// meaningless anywhere else.
func TestClientIPTrustsRealIPOnlyFromLoopback(t *testing.T) {
	proxied := httptest.NewRequest(http.MethodGet, "/api/lobby", nil)
	proxied.RemoteAddr = "127.0.0.1:5000"
	proxied.Header.Set("X-Real-IP", "203.0.113.9")
	if got := clientIP(proxied); got != "203.0.113.9" {
		t.Fatalf("loopback hop should use X-Real-IP, got %q", got)
	}

	direct := httptest.NewRequest(http.MethodGet, "/api/lobby", nil)
	direct.RemoteAddr = "198.51.100.7:5000"
	direct.Header.Set("X-Real-IP", "203.0.113.9")
	if got := clientIP(direct); got != "198.51.100.7" {
		t.Fatalf("a remote caller must not override its address, got %q", got)
	}
}

// TestAIEndpointIsRateLimited proves the engine endpoint has a tight bucket:
// burst is four, so twelve calls must trip it.
func TestAIEndpointIsRateLimited(t *testing.T) {
	srv, _ := newTestServer(t)

	for i := 0; i < 12; i++ {
		resp, err := http.Get(srv.URL + "/api/ai/move?level=1&budgetMs=50")
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			if i < 4 {
				t.Fatalf("rate limited too early (request %d)", i+1)
			}
			return
		}
	}
	t.Fatal("expected 429 within twelve requests")
}

// TestRegisterIsRateLimited covers the credential bucket.
func TestRegisterIsRateLimited(t *testing.T) {
	srv, _ := newTestServer(t)

	for i := 0; i < 12; i++ {
		body := strings.NewReader(`{"username":"user` + strconv.Itoa(i) + `","password":"password1234"}`)
		resp, err := http.Post(srv.URL+"/api/auth/register", "application/json", body)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			if i < 3 {
				t.Fatalf("registration limited too early (request %d)", i+1)
			}
			return
		}
	}
	t.Fatal("expected 429 within twelve registrations")
}
