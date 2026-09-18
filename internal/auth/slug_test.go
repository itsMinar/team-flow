package auth

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"Acme Inc":             "acme-inc",
		"  Hello   World  ":    "hello-world",
		"Foo & Bar!!!":         "foo-bar",
		"Über Corp":            "ber-corp",
		"---leading-trailing-": "leading-trailing",
		"":                     "org",
		"!!!":                  "org",
	}
	for in, want := range tests {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRandomSuffix(t *testing.T) {
	a, b := randomSuffix(), randomSuffix()
	if a == "" || b == "" {
		t.Fatal("randomSuffix returned empty string")
	}
	if a == b {
		t.Errorf("expected distinct suffixes, got %q twice", a)
	}
	if strings.ContainsAny(a, " -") {
		t.Errorf("suffix %q contains unexpected characters", a)
	}
}
