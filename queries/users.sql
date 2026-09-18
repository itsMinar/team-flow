-- name: CreateUser :one
INSERT INTO users (email, password_hash, first_name, last_name, status)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users
WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users
WHERE email = $1;

-- name: UpdateUserLastLogin :exec
UPDATE users
SET last_login_at = now()
WHERE id = $1;
