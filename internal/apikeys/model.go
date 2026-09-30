// Package apikeys implements Phase 9: organization API keys with one-time
// display, hashed storage, authentication, expiration, and revocation.
//
// An API key is an alternative credential for one (user, organization) pair. It
// authenticates requests as the user who minted it and never carries
// authorization state of its own: the key's effective permissions are
// re-resolved from that user's current membership and role on every request, so
// a role change or a removed membership takes effect immediately.
package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/validation"
)

const (
	// keyPrefixMarker makes a TeamFlow key recognizable in code review, log
	// scrubbing, and secret scanning.
	keyPrefixMarker = "tfk_"
	// keySecretBytes is the entropy of the key secret after the marker.
	keySecretBytes = 32
	// prefixLength is how much of the key is stored for display.
	prefixLength  = 12
	lastFourChars = 4

	defaultSort  = "created_at"
	sortableList = "must be one of created_at, expires_at, last_used_at"
)

// Key status values are derived, not stored: a key is active, expired, or
// revoked depending on its timestamps.
const (
	StatusActive  = "active"
	StatusExpired = "expired"
	StatusRevoked = "revoked"
)

var sortableFields = map[string]bool{"created_at": true, "expires_at": true, "last_used_at": true}

// CreateInput is the request body for minting a key.
type CreateInput struct {
	Name string
	// ExpiresInDays is optional. Zero means the configured default lifetime; a
	// value beyond the configured maximum is rejected.
	ExpiresInDays int
}

func (in CreateInput) validate() error {
	v := validation.New()
	v.Required("name", in.Name)
	v.MaxLen("name", in.Name, 100)
	v.Check(in.ExpiresInDays >= 0, "expires_in_days", "must not be negative")
	return v.Err()
}

// ListFilter narrows the organization's keys.
type ListFilter struct {
	IncludeRevoked bool
	Sort           string
	Desc           bool
}

// APIKeyDTO is the client-safe representation of a key. It deliberately carries
// only the public identifiers: the secret is available exactly once, in the
// creation response, and cannot be recovered afterwards.
type APIKeyDTO struct {
	ID             uuid.UUID  `json:"id"`
	OrganizationID uuid.UUID  `json:"organization_id"`
	CreatedBy      uuid.UUID  `json:"created_by"`
	Name           string     `json:"name"`
	KeyPrefix      string     `json:"key_prefix"`
	KeyLastFour    string     `json:"key_last_four"`
	Status         string     `json:"status"`
	ExpiresAt      time.Time  `json:"expires_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// CreatedResult is the one-time response for a freshly minted key. The secret is
// never persisted, listed, or logged, so it exists only here.
type CreatedResult struct {
	APIKey APIKeyDTO `json:"api_key"`
	// Key is the only time the secret is ever returned.
	Key string `json:"key"`
	// Warning is shown to the operator so the requirement is unmissable.
	Warning string `json:"warning"`
}

// generateKey returns a new API key and its public identifiers. The raw key is
// returned to the caller once and never stored.
func generateKey() (raw string, prefix, lastFour string, err error) {
	buf := make([]byte, keySecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generate api key: %w", err)
	}
	raw = keyPrefixMarker + base64.RawURLEncoding.EncodeToString(buf)
	return raw, raw[:prefixLength], raw[len(raw)-lastFourChars:], nil
}

// hashKey returns the SHA-256 hex digest of a key. Keys carry 256 bits of
// entropy, so a fast one-way digest is sufficient and keeps lookup by digest an
// O(1) indexed operation; a slow KDF would make it a table scan.
func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// validKeyFormat reports whether a presented key could plausibly be one of ours.
// Rejecting malformed values before hashing keeps junk out of the index.
func validKeyFormat(raw string) bool {
	return len(raw) == len(keyPrefixMarker)+43 &&
		strings.HasPrefix(raw, keyPrefixMarker) &&
		isBase64URL(raw[len(keyPrefixMarker):])
}

func isBase64URL(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// status derives the effective status of a key from its timestamps.
func status(key db.ApiKey, now time.Time) string {
	switch {
	case key.RevokedAt != nil:
		return StatusRevoked
	case now.After(key.ExpiresAt):
		return StatusExpired
	default:
		return StatusActive
	}
}

func toDTO(key db.ApiKey, now time.Time) APIKeyDTO {
	return APIKeyDTO{
		ID: key.ID, OrganizationID: key.OrganizationID, CreatedBy: key.CreatedBy,
		Name: key.Name, KeyPrefix: key.KeyPrefix, KeyLastFour: key.KeyLastFour,
		Status: status(key, now), ExpiresAt: key.ExpiresAt,
		RevokedAt: key.RevokedAt, LastUsedAt: key.LastUsedAt, CreatedAt: key.CreatedAt,
	}
}
