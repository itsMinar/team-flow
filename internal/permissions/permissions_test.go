package permissions

import (
	"slices"
	"testing"
)

func TestDefaultForRole(t *testing.T) {
	for _, role := range []string{"Owner", "Admin"} {
		if got := DefaultForRole(role); !slices.Equal(got, All()) {
			t.Fatalf("%s permissions = %v, want all", role, got)
		}
	}

	manager := DefaultForRole("Manager")
	for _, want := range []string{ProjectsCreate, ProjectsUpdate, ProjectsRead, TeamsRead, TasksRead, TasksCreate, TasksUpdate} {
		if !slices.Contains(manager, want) {
			t.Fatalf("manager missing %q", want)
		}
	}
	for _, denied := range []string{ProjectsDelete, TasksDelete, TeamsManage, RolesManage, MembersManage, OrganizationsUpdate} {
		if slices.Contains(manager, denied) {
			t.Fatalf("manager received %q", denied)
		}
	}

	for _, role := range []string{"Member", "Viewer"} {
		got := DefaultForRole(role)
		if !slices.Equal(got, readOnly) {
			t.Fatalf("%s permissions = %v, want read-only", role, got)
		}
	}
}

func TestDefaultForRoleReturnsCopies(t *testing.T) {
	viewer := DefaultForRole("Viewer")
	viewer[0] = ProjectsDelete
	if DefaultForRole("Viewer")[0] == ProjectsDelete {
		t.Fatal("DefaultForRole must not expose shared backing arrays")
	}
}
