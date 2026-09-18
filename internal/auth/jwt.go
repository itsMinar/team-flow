package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// ErrInvalidToken indicates a token that failed signature, expiry, or claim
// validation. Callers should treat it as an authentication failure.
var ErrInvalidToken = errors.New("invalid token")

const accessTokenType = "access"

// AccessClaims are the registered and custom claims carried by an access token.
// Only identifiers and metadata are included; no roles or permissions are
// embedded, so authorization always reflects current database state.
type AccessClaims struct {
	Type string `json:"type"`
	jwt.RegisteredClaims
}

// JWTService signs and verifies access tokens using an HMAC secret.
type JWTService struct {
	secret    []byte
	issuer    string
	accessTTL time.Duration
}

// NewJWTService constructs a JWTService.
func NewJWTService(secret, issuer string, accessTTL time.Duration) *JWTService {
	return &JWTService{secret: []byte(secret), issuer: issuer, accessTTL: accessTTL}
}

// GenerateAccessToken issues a signed access token for the user. tokenID (jti)
// uniquely identifies the token. It returns the signed string and its expiry.
func (s *JWTService) GenerateAccessToken(userID, tokenID uuid.UUID) (string, time.Time, error) {
	now := time.Now()
	expiresAt := now.Add(s.accessTTL)

	claims := AccessClaims{
		Type: accessTokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			ID:        tokenID.String(),
			Issuer:    s.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return signed, expiresAt, nil
}

// ParseAccessToken validates the token's signature, algorithm, expiry, issuer,
// and type, returning the parsed claims. Any failure yields ErrInvalidToken.
func (s *JWTService) ParseAccessToken(tokenString string) (*AccessClaims, error) {
	claims := &AccessClaims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("%w: unexpected signing method %v", ErrInvalidToken, t.Header["alg"])
		}
		return s.secret, nil
	},
		jwt.WithIssuer(s.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if claims.Type != accessTokenType {
		return nil, fmt.Errorf("%w: wrong token type %q", ErrInvalidToken, claims.Type)
	}
	return claims, nil
}
