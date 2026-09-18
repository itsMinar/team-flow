package auth

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)
	dashRuns     = regexp.MustCompile(`-+`)
)

// slugify converts a display name into a URL-safe slug: lowercase, ASCII
// alphanumerics separated by single hyphens, trimmed of leading/trailing dashes.
func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonSlugChars.ReplaceAllString(s, "-")
	s = dashRuns.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "org"
	}
	return s
}

// randomSuffix returns a short random hex suffix used to disambiguate slug
// collisions.
func randomSuffix() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "x"
	}
	return hex.EncodeToString(b)
}
