// Package permissions defines organization permission keys and the grants
// seeded for the default system roles.
package permissions

const (
	OrganizationsRead   = "organizations.read"
	OrganizationsUpdate = "organizations.update"
	MembersRead         = "members.read"
	MembersManage       = "members.manage"
	RolesRead           = "roles.read"
	RolesManage         = "roles.manage"
	TeamsRead           = "teams.read"
	TeamsManage         = "teams.manage"
	ProjectsRead        = "projects.read"
	ProjectsCreate      = "projects.create"
	ProjectsUpdate      = "projects.update"
	ProjectsDelete      = "projects.delete"
)

var readOnly = []string{OrganizationsRead, MembersRead, RolesRead, TeamsRead, ProjectsRead}

// All returns every permission key, in a stable order.
func All() []string {
	return []string{
		OrganizationsRead, OrganizationsUpdate,
		MembersRead, MembersManage,
		RolesRead, RolesManage,
		TeamsRead, TeamsManage,
		ProjectsRead, ProjectsCreate, ProjectsUpdate, ProjectsDelete,
	}
}

// DefaultForRole returns the permissions seeded for a system role. Migrations
// that backfill existing roles must stay in sync with this mapping.
func DefaultForRole(roleName string) []string {
	switch roleName {
	case "Owner", "Admin":
		return All()
	case "Manager":
		return append(append([]string{}, readOnly...), ProjectsCreate, ProjectsUpdate)
	default:
		return append([]string{}, readOnly...)
	}
}
