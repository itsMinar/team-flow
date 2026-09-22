package organizations

import "testing"

func TestSlugify(t *testing.T) {
	if got := slugify("Acme Inc"); got != "acme-inc" {
		t.Fatalf("slugify = %q", got)
	}
	if got := slugify("  "); got != "org" {
		t.Fatalf("blank slugify = %q", got)
	}
}
