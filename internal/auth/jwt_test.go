package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestGenerateAndParseAccessToken(t *testing.T) {
	svc := NewJWTService("test-secret", "teamflow", 15*time.Minute)
	userID := uuid.New()
	tokenID := uuid.New()

	token, expiresAt, err := svc.GenerateAccessToken(userID, tokenID)
	if err != nil {
		t.Fatalf("GenerateAccessToken error = %v", err)
	}
	if !expiresAt.After(time.Now()) {
		t.Error("expiresAt should be in the future")
	}

	claims, err := svc.ParseAccessToken(token)
	if err != nil {
		t.Fatalf("ParseAccessToken error = %v", err)
	}
	if claims.Subject != userID.String() {
		t.Errorf("subject = %q, want %q", claims.Subject, userID.String())
	}
	if claims.ID != tokenID.String() {
		t.Errorf("jti = %q, want %q", claims.ID, tokenID.String())
	}
	if claims.Type != accessTokenType {
		t.Errorf("type = %q, want %q", claims.Type, accessTokenType)
	}
}

func TestParseAccessToken_WrongSecret(t *testing.T) {
	signer := NewJWTService("secret-a", "teamflow", time.Minute)
	verifier := NewJWTService("secret-b", "teamflow", time.Minute)

	token, _, err := signer.GenerateAccessToken(uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("GenerateAccessToken error = %v", err)
	}
	if _, err := verifier.ParseAccessToken(token); err == nil {
		t.Fatal("expected error for token signed with different secret")
	}
}

func TestParseAccessToken_Expired(t *testing.T) {
	svc := NewJWTService("test-secret", "teamflow", -time.Minute) // already expired
	token, _, err := svc.GenerateAccessToken(uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("GenerateAccessToken error = %v", err)
	}
	if _, err := svc.ParseAccessToken(token); err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestParseAccessToken_RejectsAlgNone(t *testing.T) {
	svc := NewJWTService("test-secret", "teamflow", time.Minute)
	claims := AccessClaims{
		Type: accessTokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   uuid.New().String(),
			Issuer:    "teamflow",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
	}
	unsigned := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	tokenString, err := unsigned.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}
	if _, err := svc.ParseAccessToken(tokenString); err == nil {
		t.Fatal("expected error for alg=none token")
	}
}

func TestParseAccessToken_WrongIssuer(t *testing.T) {
	signer := NewJWTService("secret", "other-issuer", time.Minute)
	verifier := NewJWTService("secret", "teamflow", time.Minute)
	token, _, _ := signer.GenerateAccessToken(uuid.New(), uuid.New())
	if _, err := verifier.ParseAccessToken(token); err == nil {
		t.Fatal("expected error for wrong issuer")
	}
}

func TestParseAccessToken_RequiresExpiration(t *testing.T) {
	svc := NewJWTService("test-secret", "teamflow", time.Minute)
	claims := AccessClaims{
		Type: accessTokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: uuid.New().String(),
			ID:      uuid.New().String(),
			Issuer:  "teamflow",
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ParseAccessToken(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("got %v, want ErrInvalidToken for missing expiration", err)
	}
}
