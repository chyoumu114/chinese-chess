package ws

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xiangqi/internal/gamehall"
)

// TestCheckOriginDefaultsToSameOrigin pins the default: an empty allowlist means
// same-origin (plus the local dev server), never "anyone".
func TestCheckOriginDefaultsToSameOrigin(t *testing.T) {
	hub := newTestHub(t, gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil))

	same := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	same.Host = "example.com"
	same.Header.Set("Origin", "http://example.com")
	if !hub.checkOrigin(same) {
		t.Fatal("same-origin handshake must be accepted")
	}

	foreign := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	foreign.Host = "example.com"
	foreign.Header.Set("Origin", "https://evil.example")
	if hub.checkOrigin(foreign) {
		t.Fatal("a foreign origin must be rejected when no allowlist is set")
	}

	dev := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	dev.Host = "example.com"
	dev.Header.Set("Origin", "http://localhost:5173")
	if !hub.checkOrigin(dev) {
		t.Fatal("the local dev server should be accepted")
	}

	// Non-browser clients send no Origin and must keep working.
	noOrigin := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	noOrigin.Host = "example.com"
	if !hub.checkOrigin(noOrigin) {
		t.Fatal("clients without an Origin header must still connect")
	}
}

// TestCheckOriginHonoursAllowlist: when a list is configured it is authoritative,
// with no implicit dev-origin exemption.
func TestCheckOriginHonoursAllowlist(t *testing.T) {
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := NewHub(session, logger, []string{"https://xiangqi.example.com"})

	allowed := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	allowed.Host = "example.com"
	allowed.Header.Set("Origin", "https://xiangqi.example.com")
	if !hub.checkOrigin(allowed) {
		t.Fatal("allowlisted origin must pass")
	}

	dev := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	dev.Host = "example.com"
	dev.Header.Set("Origin", "http://localhost:5173")
	if hub.checkOrigin(dev) {
		t.Fatal("with an allowlist set, the dev origin is not implicitly allowed")
	}
}

// TestDisconnectSchedulesForfeit: going fully offline starts the abandonment
// clock for a signed-in player.
func TestDisconnectSchedulesForfeit(t *testing.T) {
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	hub := newTestHub(t, session)

	c := fakeClient(hub, 42, "bob")
	hub.add(c)
	hub.remove(c)

	hub.mu.Lock()
	_, tracked := hub.pending[42]
	hub.mu.Unlock()

	if !tracked {
		t.Fatal("a signed-in player going offline must be tracked")
	}
}

// TestForfeitSkipsPlayersWhoCameBack: a flaky network must not cost a game.
func TestForfeitSkipsPlayersWhoCameBack(t *testing.T) {
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	hub := newTestHub(t, session)
	hub.grace = 10 * time.Millisecond

	c := fakeClient(hub, 42, "bob")
	hub.add(c)
	hub.remove(c)

	// A second connection arrives before the sweep.
	hub.add(fakeClient(hub, 42, "bob"))

	time.Sleep(20 * time.Millisecond)
	hub.forfeitAbandoned(context.Background())

	hub.mu.Lock()
	_, tracked := hub.pending[42]
	hub.mu.Unlock()

	if tracked {
		t.Fatal("a reconnected player must be dropped from the forfeit queue")
	}
}

// TestForfeitIgnoresUnseated: accounts that are not in a room are simply cleared,
// with no side effects and no panic.
func TestForfeitIgnoresUnseated(t *testing.T) {
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	hub := newTestHub(t, session)
	hub.grace = 10 * time.Millisecond

	c := fakeClient(hub, 99, "guest")
	hub.add(c)
	hub.remove(c)

	time.Sleep(20 * time.Millisecond)
	hub.forfeitAbandoned(context.Background())

	hub.mu.Lock()
	_, tracked := hub.pending[99]
	hub.mu.Unlock()

	if tracked {
		t.Fatal("an unseated account must be cleared from the queue")
	}
}
