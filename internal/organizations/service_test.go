package organizations

import (
	"testing"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/db"
)

func TestSlugify(t *testing.T) {
	if got := slugify("Acme Inc"); got != "acme-inc" {
		t.Fatalf("slugify = %q", got)
	}
	if got := slugify("  "); got != "org" {
		t.Fatalf("blank slugify = %q", got)
	}
}

func TestValidateRoleRequest(t *testing.T) {
	if err := validateRoleRequest(roleRequest{Name: "Analyst", Permissions: []string{PermissionRolesRead}}); err != nil {
		t.Fatalf("valid role request: %v", err)
	}
	if err := validateRoleRequest(roleRequest{}); err == nil {
		t.Fatal("expected missing role name to fail")
	}
	if err := validateRoleRequest(roleRequest{Name: "Analyst", Permissions: make([]string, 21)}); err == nil {
		t.Fatal("expected too many permissions to fail")
	}
}

func TestRoleDTO(t *testing.T) {
	id := uuid.New()
	role := roleDTO(structRole(id), []string{PermissionRolesRead})
	if role.ID != id || len(role.Permissions) != 1 {
		t.Fatalf("unexpected role DTO: %#v", role)
	}
}

func structRole(id uuid.UUID) db.Role {
	return db.Role{ID: id, OrganizationID: uuid.New(), Name: "Analyst"}
}
