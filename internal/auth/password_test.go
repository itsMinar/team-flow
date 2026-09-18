package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("StrongPassword123!")
	if err != nil {
		t.Fatalf("HashPassword error = %v", err)
	}
	if hash == "StrongPassword123!" {
		t.Fatal("password stored in plaintext")
	}
	if !VerifyPassword(hash, "StrongPassword123!") {
		t.Error("VerifyPassword returned false for correct password")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Error("VerifyPassword returned true for incorrect password")
	}
}

func TestVerifyPassword_InvalidHash(t *testing.T) {
	if VerifyPassword("not-a-bcrypt-hash", "whatever") {
		t.Error("VerifyPassword should return false for malformed hash")
	}
}
