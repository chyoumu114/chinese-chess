// Package auth handles password storage and session tokens.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Password hashing uses PBKDF2-HMAC-SHA256 from the standard library, so the
// project needs no third-party crypto dependency. The encoded form is
//
//	pbkdf2$sha256$<iterations>$<salt-b64>$<hash-b64>
//
// which carries its own parameters and can therefore be re-tuned later without
// invalidating existing passwords.
const (
	pbkdf2Iterations = 210_000
	pbkdf2KeyLen     = 32
	pbkdf2SaltLen    = 16
	hashPrefix       = "pbkdf2$sha256$"
)

// ErrInvalidHash is returned when a stored hash cannot be parsed.
var ErrInvalidHash = errors.New("auth: malformed password hash")

// HashPassword derives a storable hash for the password.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("auth: password must not be empty")
	}
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: reading salt: %w", err)
	}
	sum := pbkdf2SHA256([]byte(password), salt, pbkdf2Iterations, pbkdf2KeyLen)
	return fmt.Sprintf("%s%d$%s$%s", hashPrefix, pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum)), nil
}

// VerifyPassword reports whether the password matches the stored hash. It runs
// in constant time with respect to the hash contents.
func VerifyPassword(password, encoded string) (bool, error) {
	iterations, salt, want, err := parseHash(encoded)
	if err != nil {
		return false, err
	}
	got := pbkdf2SHA256([]byte(password), salt, iterations, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parseHash(encoded string) (iterations int, salt, hash []byte, err error) {
	if !strings.HasPrefix(encoded, hashPrefix) {
		return 0, nil, nil, ErrInvalidHash
	}
	parts := strings.Split(strings.TrimPrefix(encoded, hashPrefix), "$")
	if len(parts) != 3 {
		return 0, nil, nil, ErrInvalidHash
	}
	iterations, err = strconv.Atoi(parts[0])
	if err != nil || iterations < 1000 {
		return 0, nil, nil, ErrInvalidHash
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil || len(salt) == 0 {
		return 0, nil, nil, ErrInvalidHash
	}
	hash, err = base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(hash) == 0 {
		return 0, nil, nil, ErrInvalidHash
	}
	return iterations, salt, hash, nil
}

// pbkdf2SHA256 implements PBKDF2 (RFC 2898) with HMAC-SHA256 as the PRF.
func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	mac := hmac.New(sha256.New, password)
	hashLen := mac.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	derived := make([]byte, 0, blocks*hashLen)
	buf := make([]byte, 4)

	for block := 1; block <= blocks; block++ {
		mac.Reset()
		mac.Write(salt)
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		mac.Write(buf[:4])
		sum := mac.Sum(nil)

		u := make([]byte, len(sum))
		copy(u, sum)
		for i := 1; i < iterations; i++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(u[:0])
			for j := range sum {
				sum[j] ^= u[j]
			}
		}
		derived = append(derived, sum...)
	}
	return derived[:keyLen]
}

// RandomToken returns a URL-safe random string with n bytes of entropy.
func RandomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
