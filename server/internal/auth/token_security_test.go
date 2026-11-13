package auth

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// craftToken signs an arbitrary claims set, which lets these tests describe
// tokens the issuer itself would never produce.
func craftToken(t *testing.T, issuer *TokenIssuer, claims Claims) string {
	t.Helper()
	h, err := json.Marshal(header{Alg: "HS256", Typ: "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	p, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signingInput := b64(h) + "." + b64(p)
	return signingInput + "." + b64(issuer.sign(signingInput))
}

// TestParseRequiresExpiry: a token without exp used to be accepted and would
// then never expire. It must be rejected as malformed.
func TestParseRequiresExpiry(t *testing.T) {
	issuer, err := NewTokenIssuer("secret-0123456789abcdef", time.Hour)
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}

	token := craftToken(t, issuer, Claims{Subject: 1, Username: "u"})
	if _, err := issuer.Parse(token); !errors.Is(err, ErrTokenMalformed) {
		t.Fatalf("a token without exp must be malformed, got %v", err)
	}
}

// TestParseRejectsOverlongTTL: there is no revocation list, so a long-lived
// token is a long-lived liability. Lifetimes beyond the ceiling are refused.
func TestParseRejectsOverlongTTL(t *testing.T) {
	issuer, err := NewTokenIssuer("secret-0123456789abcdef", time.Hour)
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}

	now := time.Now()
	token := craftToken(t, issuer, Claims{
		Subject:  1,
		Username: "u",
		IssuedAt: now.Unix(),
		Expires:  now.Add(60 * 24 * time.Hour).Unix(),
	})
	if _, err := issuer.Parse(token); !errors.Is(err, ErrTokenMalformed) {
		t.Fatalf("a 60-day token must be rejected, got %v", err)
	}
}

// TestIssuedTokenRoundTrips keeps the happy path honest: the tightening above
// must not break tokens the issuer produces itself.
func TestIssuedTokenRoundTrips(t *testing.T) {
	issuer, err := NewTokenIssuer("secret-0123456789abcdef", time.Hour)
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}

	token, _, err := issuer.Issue(7, "alice")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := issuer.Parse(token)
	if err != nil {
		t.Fatalf("issued token should parse: %v", err)
	}
	if claims.Subject != 7 || claims.Username != "alice" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}
