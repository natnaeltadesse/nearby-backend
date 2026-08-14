-- name: CreateVerificationCode :one
INSERT INTO verification_codes (user_id, channel, destination, code_hash, expires_at)
VALUES (@user_id, @channel, lower(@destination), @code_hash, @expires_at)
RETURNING id, user_id, channel, destination, attempts, expires_at, consumed_at, created_at;

-- Issuing a new code retires every earlier one for that address, so a resend
-- cannot leave two codes working at once.
-- name: ConsumeVerificationCodesFor :exec
UPDATE verification_codes
SET consumed_at = now()
WHERE channel = @channel AND destination = lower(@destination) AND consumed_at IS NULL;

-- name: GetLiveVerificationCode :one
SELECT id, user_id, channel, destination, code_hash, attempts, expires_at,
       consumed_at, created_at
FROM verification_codes
WHERE channel = @channel AND destination = lower(@destination) AND consumed_at IS NULL
ORDER BY created_at DESC
LIMIT 1;

-- name: IncrementVerificationAttempts :one
UPDATE verification_codes
SET attempts = attempts + 1
WHERE id = @id
RETURNING attempts;

-- Guarded on consumed_at IS NULL so two simultaneous submissions of the same
-- code cannot both succeed; the loser sees zero rows and is treated as stale.
-- name: ConsumeVerificationCode :execrows
UPDATE verification_codes
SET consumed_at = now()
WHERE id = @id AND consumed_at IS NULL;

-- name: MarkEmailVerified :exec
UPDATE users
SET email_verified = true, updated_at = now()
WHERE id = @id;

-- name: DeleteExpiredVerificationCodes :execrows
DELETE FROM verification_codes
WHERE expires_at < now() - interval '7 days';
