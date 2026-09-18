package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// refreshTokenBytes is the entropy of a raw refresh token before encoding.
const refreshTokenBytes = 32

// generateRefreshToken returns a cryptographically random, URL-safe refresh
// token. The raw value is returned to the client exactly once and never stored.
func generateRefreshToken() (string, error) {
	buf := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken returns the SHA-256 hex digest of a token. Refresh tokens are high
// entropy, so a fast one-way hash is sufficient (and enables O(1) lookup by
// hash); bcrypt is unnecessary here and would prevent indexed lookups.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
