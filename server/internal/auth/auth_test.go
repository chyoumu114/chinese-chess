package auth

import (
	"strings"
	"testing"
	"time"
)

// TestPasswordRoundTrip checks hashing and verification, including that the same
// password produces different hashes (so a stolen table cannot be attacked with
// a single precomputation) and that the encoded form carries its parameters.
func TestPasswordRoundTrip(t *testing.T) {
	const password = "correct horse battery staple"

	first, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if first == second {
		t.Fatalf("two hashes of the same password are identical, so the salt is not random")
	}
	if !strings.HasPrefix(first, "pbkdf2$sha256$") {
		t.Errorf("hash is not self-describing: %q", first)
	}
	if strings.Contains(first, password) {
		t.Fatalf("the hash contains the password in clear")
	}

	ok, err := VerifyPassword(password, first)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Errorf("the correct password was rejected")
	}
	ok, err = VerifyPassword("wrong password", first)
	if err != nil {
		t.Fatalf("VerifyPassword(wrong): %v", err)
	}
	if ok {
		t.Errorf("a wrong password was accepted")
	}

	// A unicode password must round-trip: this is a Chinese-language product.
	const unicode = "密码-テスト-🔒"
	hash, err := HashPassword(unicode)
	if err != nil {
		t.Fatalf("HashPassword(unicode): %v", err)
	}
	ok, err = VerifyPassword(unicode, hash)
	if err != nil || !ok {
		t.Errorf("a unicode password did not verify (ok=%v err=%v)", ok, err)
	}
	if ok, _ := VerifyPassword("密码", hash); ok {
		t.Errorf("a prefix of the unicode password was accepted")
	}
}

// TestVerifyRejectsMalformedHashes checks the parser cannot be tricked.
func TestVerifyRejectsMalformedHashes(t *testing.T) {
	for _, bad := range []string{
		"",
		"plaintext",
		"pbkdf2$sha256$",
		"pbkdf2$sha256$notanumber$c2FsdA$aGFzaA",
		"pbkdf2$sha256$100$c2FsdA$aGFzaA", // iteration count below the floor
		"pbkdf2$sha256$210000$$aGFzaA",    // empty salt
		"pbkdf2$sha256$210000$c2FsdA$",    // empty hash
		"bcrypt$whatever$1$2$3",           // wrong algorithm
		"pbkdf2$sha256$210000$!!!$aGFzaA", // invalid base64
	} {
		if _, err := VerifyPassword("anything", bad); err == nil {
			t.Errorf("VerifyPassword accepted the malformed hash %q", bad)
		}
	}
}

// TestHashPasswordRejectsEmpty checks the guard.
func TestHashPasswordRejectsEmpty(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Errorf("HashPassword(\"\") should fail")
	}
}

// TestTokenRoundTrip checks issue and parse.
func TestTokenRoundTrip(t *testing.T) {
	issuer, err := NewTokenIssuer("a-test-secret-that-is-long-enough", time.Hour)
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	token, exp, err := issuer.Issue(42, "alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token == "" {
		t.Fatalf("Issue returned an empty token")
	}
	if time.Until(exp) <= 0 {
		t.Errorf("expiry %v is not in the future", exp)
	}
	if got := strings.Count(token, "."); got != 2 {
		t.Errorf("token has %d dots, want 2 (header.payload.signature)", got)
	}

	claims, err := issuer.Parse(token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.Subject != 42 || claims.Username != "alice" {
		t.Errorf("claims = %+v, want subject 42 and username alice", claims)
	}
}

// TestTokenRejections covers every way a token can be invalid.
func TestTokenRejections(t *testing.T) {
	issuer, err := NewTokenIssuer("a-test-secret-that-is-long-enough", time.Hour)
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	good, _, err := issuer.Issue(1, "alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// A different key must not validate.
	other, err := NewTokenIssuer("a-completely-different-secret-key", time.Hour)
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	if _, err := other.Parse(good); err == nil {
		t.Errorf("a token signed with another key was accepted")
	}

	// Tampering with the payload must invalidate the signature.
	parts := strings.Split(good, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected token shape")
	}
	tampered := parts[0] + "." + parts[1] + "x." + parts[2]
	if _, err := issuer.Parse(tampered); err == nil {
		t.Errorf("a token with a modified payload was accepted")
	}
	// Tampering with the signature must be caught.
	if _, err := issuer.Parse(parts[0] + "." + parts[1] + "." + parts[2] + "x"); err == nil {
		t.Errorf("a token with a modified signature was accepted")
	}

	for _, bad := range []string{"", "a", "a.b", "a.b.c.d", "not.a.token", "...", strings.Repeat("x", 200)} {
		if _, err := issuer.Parse(bad); err == nil {
			t.Errorf("Parse(%q) should have failed", bad)
		}
	}

	// An expired token must be rejected. The registered claims are whole seconds,
	// so the clock must cross a full second boundary before expiry is visible.
	shortLived, err := NewTokenIssuer("a-test-secret-that-is-long-enough", time.Millisecond)
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	expired, _, err := shortLived.Issue(1, "alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := shortLived.Parse(expired); err == nil {
		t.Errorf("an expired token was accepted")
	}
}

// TestNewTokenIssuerValidation checks the configuration guards.
func TestNewTokenIssuerValidation(t *testing.T) {
	if _, err := NewTokenIssuer("short", time.Hour); err == nil {
		t.Errorf("a short signing secret should be refused")
	}
	if _, err := NewTokenIssuer("a-test-secret-that-is-long-enough", 0); err == nil {
		t.Errorf("a zero TTL should be refused")
	}
	if _, err := NewTokenIssuer("a-test-secret-that-is-long-enough", -time.Minute); err == nil {
		t.Errorf("a negative TTL should be refused")
	}
}

// TestParseBearer covers the header helper, which is what the API middleware and
// the WebSocket handshake both rely on.
func TestParseBearer(t *testing.T) {
	for _, ok := range []string{"Bearer abc", "bearer abc", "BEARER abc", "Bearer  abc  "} {
		token, found := ParseBearer(ok)
		if !found || token != "abc" {
			t.Errorf("ParseBearer(%q) = %q, %v; want abc, true", ok, token, found)
		}
	}
	for _, bad := range []string{"", "Bearer", "Bearer ", "abc", "Basic abc", "Bearera bc"} {
		if token, found := ParseBearer(bad); found {
			t.Errorf("ParseBearer(%q) = %q, true; want false", bad, token)
		}
	}
}

// TestRandomToken checks the entropy helper used for one-off secrets.
func TestRandomToken(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		tok, err := RandomToken(16)
		if err != nil {
			t.Fatalf("RandomToken: %v", err)
		}
		if tok == "" {
			t.Fatalf("RandomToken returned an empty string")
		}
		if seen[tok] {
			t.Fatalf("RandomToken repeated a value: %q", tok)
		}
		seen[tok] = true
	}
}
