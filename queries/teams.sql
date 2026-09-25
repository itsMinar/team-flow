-- name: CreateTeam :one
INSERT INTO teams (organization_id, name, description, created_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetTeam :one
SELECT * FROM teams
WHERE id = $1 AND organization_id = $2;

-- name: ListTeams :many
SELECT * FROM teams
WHERE organization_id = $1
ORDER BY name;

-- name: UpdateTeam :one
UPDATE teams
SET name = $3, description = $4
WHERE id = $1 AND organization_id = $2
RETURNING *;

-- name: DeleteTeam :exec
DELETE FROM teams
WHERE id = $1 AND organization_id = $2;

-- name: ListTeamMembers :many
SELECT tm.id, tm.organization_id, tm.team_id, tm.user_id, tm.created_at,
       u.email, u.first_name, u.last_name
FROM team_members tm
JOIN users u ON u.id = tm.user_id
WHERE tm.team_id = $1 AND tm.organization_id = $2
ORDER BY u.email;

-- name: AddTeamMember :one
INSERT INTO team_members (organization_id, team_id, user_id)
VALUES ($1, $2, $3)
RETURNING *;

-- name: RemoveTeamMember :exec
DELETE FROM team_members
WHERE id = $1 AND team_id = $2 AND organization_id = $3;