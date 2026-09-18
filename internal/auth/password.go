package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost balances security against latency. The default (10) is acceptable;
// 12 is a stronger production setting with a still-reasonable hashing time.
const bcryptCost = 12

// HashPassword returns a bcrypt hash of the plaintext password. bcrypt includes
// a per-hash salt, so no separate salt storage is needed.
func HashPassword(plaintext string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword reports whether plaintext matches the stored bcrypt hash. It
// returns false for any mismatch without revealing why.
func VerifyPassword(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}
