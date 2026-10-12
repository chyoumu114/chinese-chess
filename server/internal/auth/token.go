package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Session tokens are signed with HMAC-SHA256 in the compact JWT shape
// (header.payload.signature). Verification is a signature check plus a handful
// of registered-claim checks, which is all this service needs; no third-party
// JWT library is required.

// Errors returned by token verification.
var (
	ErrTokenMalformed = errors.New("auth: malformed token")
	ErrTokenSignature = errors.New("auth: bad token signature")
	ErrTokenExpired   = errors.New("auth: token expired")
	ErrTokenNotYet    = errors.New("auth: token not valid yet")
	ErrTokenAlg       = errors.New("auth: unsupported token algorithm")
)

// Claims is the payload carried by a session token.
type Claims struct {
	Subject   int64  `json:"sub"`
	Username  string `json:"usr"`
	IssuedAt  int64  `json:"iat"`
	Expires   int64  `json:"exp"`
	NotBefore int64  `json:"nbf,omitempty"`
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// TokenIssuer signs and verifies session tokens.
type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
}

// NewTokenIssuer returns an issuer signing with secret and issuing tokens that
// live for ttl.
func NewTokenIssuer(secret string, ttl time.Duration) (*TokenIssuer, error) {
	if len(secret) < 16 {
		return nil, fmt.Errorf("auth: signing secret must be at least 16 bytes, got %d", len(secret))
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("auth: token TTL must be positive")
	}
	return &TokenIssuer{secret: []byte(secret), ttl: ttl}, nil
}

// TTL returns the configured token lifetime.
func (t *TokenIssuer) TTL() time.Duration { return t.ttl }

// Issue signs a token for the user.
func (t *TokenIssuer) Issue(userID int64, username string) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(t.ttl)
	claims := Claims{
		Subject:   userID,
		Username:  username,
		IssuedAt:  now.Unix(),
		Expires:   exp.Unix(),
		NotBefore: now.Unix(),
	}
	headerJSON, err := json.Marshal(header{Alg: "HS256", Typ: "JWT"})
	if err != nil {
		return "", time.Time{}, err
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, err
	}
	signingInput := b64(headerJSON) + "." + b64(payloadJSON)
	return signingInput + "." + b64(t.sign(signingInput)), exp, nil
}

// Parse verifies a token and returns its claims.
func (t *TokenIssuer) Parse(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrTokenMalformed
	}
	headerJSON, err := unb64(parts[0])
	if err != nil {
		return Claims{}, ErrTokenMalformed
	}
	var h header
	if err := json.Unmarshal(headerJSON, &h); err != nil {
		return Claims{}, ErrTokenMalformed
	}
	if h.Alg != "HS256" {
		return Claims{}, ErrTokenAlg
	}

	want := t.sign(parts[0] + "." + parts[1])
	got, err := unb64(parts[2])
	if err != nil {
		return Claims{}, ErrTokenMalformed
	}
	if subtle.ConstantTimeCompare(want, got) != 1 {
		return Claims{}, ErrTokenSignature
	}

	payloadJSON, err := unb64(parts[1])
	if err != nil {
		return Claims{}, ErrTokenMalformed
	}
	var claims Claims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return Claims{}, ErrTokenMalformed
	}
	now := time.Now().Unix()
	if claims.Expires != 0 && now > claims.Expires {
		return Claims{}, ErrTokenExpired
	}
	if claims.NotBefore != 0 && now < claims.NotBefore {
		return Claims{}, ErrTokenNotYet
	}
	if claims.Subject == 0 {
		return Claims{}, ErrTokenMalformed
	}
	return claims, nil
}

func (t *TokenIssuer) sign(signingInput string) []byte {
	mac := hmac.New(sha256.New, t.secret)
	mac.Write([]byte(signingInput))
	return mac.Sum(nil)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// ParseBearer extracts a token from an Authorization header value.
func ParseBearer(headerValue string) (string, bool) {
	const prefix = "bearer "
	if len(headerValue) <= len(prefix) {
		return "", false
	}
	if !strings.EqualFold(headerValue[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(headerValue[len(prefix):])
	return token, token != ""
}

// ParseUserID is a convenience for tests and tooling.
func ParseUserID(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }
