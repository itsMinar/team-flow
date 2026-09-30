// Package invitations implements Phase 8: organization invitations with hashed
// single-use tokens, expiration, resend, revoke, and acceptance.
package invitations

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/validation"
)

// Invitation statuses. A pending invitation past its expiry is reported as
// expired even before the row is updated, so lists never show stale pending
// invitations.
const (
	StatusPending  = "pending"
	StatusAccepted = "accepted"
	StatusRevoked  = "revoked"
	StatusExpired  = "expired"
)

const (
	// tokenBytes is the entropy of a raw invitation token. Invitation tokens
	// are bearer credentials for joining an organization, so they use the same
	// 256-bit budget as refresh tokens.
	tokenBytes = 32
	// acceptPath is the frontend route that renders the accept-invitation page.
	acceptPath = "/invitations/accept?token=%s"

	ownerRoleName = "Owner"
)

var (
	validStatuses  = map[string]bool{StatusPending: true, StatusAccepted: true, StatusRevoked: true, StatusExpired: true}
	sortableFields = map[string]bool{"created_at": true, "expires_at": true, "email": true}
	defaultSort    = "created_at"
	statusList     = "must be one of pending, accepted, revoked, expired"
	sortableList   = "must be one of created_at, expires_at, email"
)

// CreateInput is the request body for inviting a new member.
type CreateInput struct {
	Email  string
	RoleID uuid.UUID
}

func (in CreateInput) validate() error {
	v := validation.New()
	v.Required("email", in.Email)
	v.Email("email", in.Email)
	v.Check(in.RoleID != uuid.Nil, "role_id", "is required")
	return v.Err()
}

// ListFilter narrows the organization's invitations.
type ListFilter struct {
	Status *string
	Email  *string
	Sort   string
	Desc   bool
}

// InvitationDTO is the client-safe representation of an invitation. The token
// itself is never included: only its hash is stored, so it cannot be recovered.
type InvitationDTO struct {
	ID             uuid.UUID  `json:"id"`
	OrganizationID uuid.UUID  `json:"organization_id"`
	Email          string     `json:"email"`
	RoleID         uuid.UUID  `json:"role_id"`
	RoleName       string     `json:"role_name,omitempty"`
	Status         string     `json:"status"`
	InvitedBy      uuid.UUID  `json:"invited_by"`
	ExpiresAt      time.Time  `json:"expires_at"`
	AcceptedAt     *time.Time `json:"accepted_at,omitempty"`
	AcceptedBy     *uuid.UUID `json:"accepted_by,omitempty"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// PreviewDTO is the safe, public description of an invitation shown to the
// invitee before they accept. It is intentionally limited to what the token
// holder already knows from the email itself.
type PreviewDTO struct {
	OrganizationID   uuid.UUID  `json:"organization_id"`
	OrganizationName string     `json:"organization_name"`
	Email            string     `json:"email"`
	RoleName         string     `json:"role_name"`
	Status           string     `json:"status"`
	ExpiresAt        time.Time  `json:"expires_at"`
	AccountExists    bool       `json:"account_exists"`
	AcceptedBy       *uuid.UUID `json:"accepted_by,omitempty"`
}

// AcceptInput accepts an invitation for someone who does not have an account
// yet. Password, FirstName, and LastName are required only on that path; an
// authenticated caller accepting their own invitation sends only the token.
type AcceptInput struct {
	Token     string
	Password  string
	FirstName string
	LastName  string
}

// validateAccount reports whether the account-creation fields are usable. An
// empty password means the caller is expected to already be authenticated.
func (in AcceptInput) validateAccount() error {
	if in.Password == "" && in.FirstName == "" && in.LastName == "" {
		return nil
	}
	v := validation.New()
	v.Password("password", in.Password)
	v.Required("first_name", in.FirstName)
	v.MaxLen("first_name", in.FirstName, 100)
	v.Required("last_name", in.LastName)
	v.MaxLen("last_name", in.LastName, 100)
	return v.Err()
}

// AcceptResult is returned on successful acceptance. A session is always
// issued, so the invitee is signed in immediately and never has to log in
// separately after joining an organization.
type AcceptResult struct {
	InvitationID     uuid.UUID
	OrganizationID   uuid.UUID
	OrganizationName string
	RoleName         string
	User             auth.UserDTO
	Tokens           auth.TokenPair
}

// generateToken returns a cryptographically random, URL-safe invitation token.
// The raw value is returned once and never stored.
func generateToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate invitation token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken returns the SHA-256 hex digest of a token. Invitation tokens have
// 256 bits of entropy, so a fast one-way digest is sufficient and keeps lookup
// by hash an O(1) indexed operation; a slow KDF would add nothing here and
// prevent indexed lookups entirely.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// acceptURL builds the link emailed to the invitee. The token is put in a query
// parameter because the route is a frontend page, not an API path.
func acceptURL(baseURL, token string) string {
	return strings.TrimRight(baseURL, "/") + fmt.Sprintf(acceptPath, token)
}

// effectiveStatus reports the status an invitation should be displayed with.
// A pending invitation whose expiry has passed is reported as expired; the row
// is updated lazily, when it is next acted upon, to avoid a sweep job.
func effectiveStatus(inv db.Invitation, now time.Time) string {
	if inv.Status == StatusPending && now.After(inv.ExpiresAt) {
		return StatusExpired
	}
	return inv.Status
}

func isUsable(inv db.Invitation, now time.Time) bool {
	return inv.Status == StatusPending && !now.After(inv.ExpiresAt)
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func toDTO(inv db.Invitation, roleName string, now time.Time) InvitationDTO {
	return InvitationDTO{
		ID: inv.ID, OrganizationID: inv.OrganizationID, Email: inv.Email,
		RoleID: inv.RoleID, RoleName: roleName, Status: effectiveStatus(inv, now),
		InvitedBy: inv.InvitedBy, ExpiresAt: inv.ExpiresAt,
		AcceptedAt: inv.AcceptedAt, AcceptedBy: inv.AcceptedBy, RevokedAt: inv.RevokedAt,
		CreatedAt: inv.CreatedAt, UpdatedAt: inv.UpdatedAt,
	}
}
