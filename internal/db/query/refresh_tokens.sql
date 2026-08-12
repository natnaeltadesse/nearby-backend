-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (user_id, token_hash, client_type, expires_at)
VALUES (@user_id, @token_hash, @client_type, @expires_at)
RETURNING id, user_id, token_hash, client_type, expires_at, revoked_at, created_at;

-- name: GetRefreshTokenByHash :one
SELECT id, user_id, token_hash, client_type, expires_at, revoked_at, created_at
FROM refresh_tokens
WHERE token_hash = @token_hash;

-- Returns the number of rows it changed, which is how rotation stays safe
-- under concurrency: two simultaneous refreshes with the same token both pass
-- the read, but only one of them can move revoked_at from NULL.
-- name: RevokeRefreshToken :execrows
UPDATE refresh_tokens
SET revoked_at = now()
WHERE id = @id AND revoked_at IS NULL;

-- name: RevokeRefreshTokenByHash :one
UPDATE refresh_tokens
SET revoked_at = now()
WHERE token_hash = @token_hash AND revoked_at IS NULL
RETURNING id, user_id;

-- Used on sign-out-everywhere and on refresh-token reuse detection.
-- name: RevokeAllUserRefreshTokens :exec
UPDATE refresh_tokens
SET revoked_at = now()
WHERE user_id = @user_id AND revoked_at IS NULL;

-- name: DeleteExpiredRefreshTokens :execrows
DELETE FROM refresh_tokens
WHERE expires_at < now() - interval '30 days';
