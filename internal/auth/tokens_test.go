package auth

import "testing"

func TestGenerateRefreshToken_UniqueAndURLSafe(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		tok, err := generateRefreshToken()
		if err != nil {
			t.Fatalf("generateRefreshToken error = %v", err)
		}
		if tok == "" {
			t.Fatal("empty token")
		}
		if seen[tok] {
			t.Fatalf("duplicate token generated: %q", tok)
		}
		seen[tok] = true
	}
}

func TestHashToken_Deterministic(t *testing.T) {
	h1 := hashToken("abc")
	h2 := hashToken("abc")
	if h1 != h2 {
		t.Error("hashToken not deterministic")
	}
	if h1 == "abc" {
		t.Error("hashToken returned raw value")
	}
	if hashToken("abc") == hashToken("abd") {
		t.Error("different inputs produced same hash")
	}
	if len(h1) != 64 {
		t.Errorf("sha256 hex length = %d, want 64", len(h1))
	}
}
