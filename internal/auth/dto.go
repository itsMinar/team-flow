package auth

import (
	"time"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/db"
)

// RegisterInput is the validated input for registration.
type RegisterInput struct {
	Email            string
	Password         string
	FirstName        string
	LastName         string
	OrganizationName string
}

// LoginInput is the validated input for login.
type LoginInput struct {
	Email    string
	Password string
}

// RequestMeta carries request metadata associated with a session for auditing
// and reuse detection.
type RequestMeta struct {
	UserAgent string
	IPAddress string
}

// TokenPair is a freshly issued access + refresh token pair.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	ExpiresAt    time.Time
}

// AuthResult is returned by register/login/refresh: the token pair plus the
// authenticated user.
type AuthResult struct {
	Tokens TokenPair
	User   UserDTO
}

// UserDTO is the client-safe representation of a user. It never includes the
// password hash.
type UserDTO struct {
	ID              uuid.UUID  `json:"id"`
	Email           string     `json:"email"`
	FirstName       string     `json:"first_name"`
	LastName        string     `json:"last_name"`
	AvatarURL       *string    `json:"avatar_url,omitempty"`
	Status          string     `json:"status"`
	EmailVerifiedAt *time.Time `json:"email_verified_at,omitempty"`
	LastLoginAt     *time.Time `json:"last_login_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

// newUserDTO maps a database user to the client-safe DTO, dropping the password
// hash and any other sensitive fields.
func newUserDTO(u db.User) UserDTO {
	return UserDTO{
		ID:              u.ID,
		Email:           u.Email,
		FirstName:       u.FirstName,
		LastName:        u.LastName,
		AvatarURL:       u.AvatarUrl,
		Status:          u.Status,
		EmailVerifiedAt: u.EmailVerifiedAt,
		LastLoginAt:     u.LastLoginAt,
		CreatedAt:       u.CreatedAt,
	}
}
