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
	TasksRead           = "tasks.read"
	TasksCreate         = "tasks.create"
	TasksUpdate         = "tasks.update"
	TasksDelete         = "tasks.delete"
	APIKeysManage       = "api_keys.manage"
	AuditRead           = "audit.read"
)

var readOnly = []string{OrganizationsRead, MembersRead, RolesRead, TeamsRead, ProjectsRead, TasksRead}

// All returns every permission key, in a stable order.
func All() []string {
	return []string{
		OrganizationsRead, OrganizationsUpdate,
		MembersRead, MembersManage,
		RolesRead, RolesManage,
		TeamsRead, TeamsManage,
		ProjectsRead, ProjectsCreate, ProjectsUpdate, ProjectsDelete,
		TasksRead, TasksCreate, TasksUpdate, TasksDelete,
		APIKeysManage,
		AuditRead,
	}
}

// DefaultForRole returns the permissions seeded for a system role. Migrations
// that backfill existing roles must stay in sync with this mapping.
func DefaultForRole(roleName string) []string {
	switch roleName {
	case "Owner", "Admin":
		return All()
	case "Manager":
		return append(append([]string{}, readOnly...),
			ProjectsCreate, ProjectsUpdate,
			TasksCreate, TasksUpdate,
		)
	default:
		return append([]string{}, readOnly...)
	}
}
