package config

import "testing"

// TestValidateJWTSecret pins the production checklist for the signing key:
// long enough, not a shipped placeholder, and not a single repeated character.
func TestValidateJWTSecret(t *testing.T) {
	reject := map[string]string{
		"too short":   "short-secret",
		"placeholder": "dev-insecure-secret-change-me",
		"change-me":   "change-me-please-0123456789abcdef",
		"low entropy": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"common word": "this-is-my-super-secret-password-1",
	}
	for name, secret := range reject {
		if err := validateJWTSecret(secret); err == nil {
			t.Errorf("%s: %q should be rejected", name, secret)
		}
	}

	if err := validateJWTSecret("9f2c1d7b4e8a3c5f6b0d2a9e7c41f8b3"); err != nil {
		t.Errorf("a random 32-byte secret should pass: %v", err)
	}
}
