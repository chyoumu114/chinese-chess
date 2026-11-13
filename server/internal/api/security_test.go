package api

import (
	"net/http"
	"testing"

	"xiangqi/internal/config"
)

// TestSecurityHeadersPresent checks the browser-facing hardening headers.
func TestSecurityHeadersPresent(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	for _, name := range []string{
		"X-Content-Type-Options",
		"X-Frame-Options",
		"Referrer-Policy",
		"Permissions-Policy",
		"Content-Security-Policy",
	} {
		if resp.Header.Get(name) == "" {
			t.Fatalf("missing security header %s", name)
		}
	}
	// HSTS must not be sent over plain HTTP: browsers ignore it, and it would
	// only mislead operators debugging a local setup.
	if h := resp.Header.Get("Strict-Transport-Security"); h != "" {
		t.Fatalf("HSTS must not be set over plain HTTP, got %q", h)
	}
}

// TestOriginAllowedPolicy pins the CORS decision table. The important property
// is the default: an empty allow-list means same-origin, never "anyone".
func TestOriginAllowedPolicy(t *testing.T) {
	prod := &Server{cfg: config.Config{Env: "production"}}
	if prod.originAllowed("https://evil.example") {
		t.Fatal("production must reject an unlisted origin")
	}
	if prod.originAllowed("http://localhost:5173") {
		t.Fatal("production must not accept the dev-server origin")
	}

	dev := &Server{cfg: config.Config{Env: "development"}}
	if !dev.originAllowed("http://localhost:5173") {
		t.Fatal("development should accept the local dev server")
	}
	if dev.originAllowed("https://evil.example") {
		t.Fatal("development must not accept arbitrary origins")
	}

	listed := &Server{cfg: config.Config{
		Env:            "production",
		AllowedOrigins: []string{"https://xiangqi.example.com"},
	}}
	if !listed.originAllowed("https://xiangqi.example.com") {
		t.Fatal("a configured origin should pass")
	}
	if listed.originAllowed("https://evil.example") {
		t.Fatal("an unlisted origin must fail even when a list exists")
	}
}

// TestCORSDeniedOriginGetsNoHeader checks the header actually follows the
// policy, not just the predicate.
func TestCORSDeniedOriginGetsNoHeader(t *testing.T) {
	srv, _ := newTestServer(t)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/health", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("denied origin must not be echoed back, got %q", got)
	}
}

// TestCORSAllowsLocalDevOrigin keeps the documented local workflow working.
func TestCORSAllowsLocalDevOrigin(t *testing.T) {
	srv, _ := newTestServer(t)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/health", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Origin", "http://localhost:5173")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("dev origin should be echoed, got %q", got)
	}
	if resp.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("Allow-Credentials should not be advertised")
	}
}
