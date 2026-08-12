-- name: CreateUser :one
INSERT INTO users (name, email, phone, password_hash, platform_role)
VALUES (@name, lower(@email), sqlc.narg(phone), @password_hash, @platform_role)
RETURNING id, name, email, phone, platform_role, email_verified, fcm_token, created_at, updated_at;

-- name: GetUserByEmail :one
SELECT id, name, email, phone, password_hash, platform_role, email_verified,
       fcm_token, created_at, updated_at
FROM users
WHERE email = lower(@email);

-- name: GetUserByID :one
SELECT id, name, email, phone, platform_role, email_verified, fcm_token,
       created_at, updated_at
FROM users
WHERE id = @id;

-- name: UpdateUserProfile :one
UPDATE users
SET name  = COALESCE(sqlc.narg(name), name),
    phone = COALESCE(sqlc.narg(phone), phone),
    updated_at = now()
WHERE id = @id
RETURNING id, name, email, phone, platform_role, email_verified, fcm_token,
          created_at, updated_at;

-- name: SetUserFCMToken :exec
UPDATE users
SET fcm_token = sqlc.narg(fcm_token), updated_at = now()
WHERE id = @id;

-- name: ListUsers :many
SELECT id, name, email, phone, platform_role, email_verified, created_at, updated_at
FROM users
WHERE (@search::text = '' OR name ILIKE '%' || @search || '%' OR email ILIKE '%' || @search || '%')
ORDER BY created_at DESC
LIMIT @result_limit OFFSET @result_offset;

-- name: CountUsers :one
SELECT count(*) FROM users
WHERE (@search::text = '' OR name ILIKE '%' || @search || '%' OR email ILIKE '%' || @search || '%');
