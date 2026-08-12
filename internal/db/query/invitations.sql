-- name: CreateInvitation :one
INSERT INTO invitations (provider_id, email, role, token_hash, expires_at)
VALUES (@provider_id, lower(@email), @role, @token_hash, @expires_at)
RETURNING id, provider_id, email, role, expires_at, accepted_at, created_at;

-- name: GetInvitationByTokenHash :one
SELECT id, provider_id, email, role, token_hash, expires_at, accepted_at, created_at
FROM invitations
WHERE token_hash = @token_hash;

-- name: AcceptInvitation :one
UPDATE invitations
SET accepted_at = now()
WHERE id = @id AND accepted_at IS NULL AND expires_at > now()
RETURNING id, provider_id, email, role, expires_at, accepted_at, created_at;

-- name: ListInvitations :many
SELECT id, provider_id, email, role, expires_at, accepted_at, created_at
FROM invitations
WHERE provider_id = @provider_id
ORDER BY created_at DESC;

-- name: DeleteInvitation :execrows
DELETE FROM invitations
WHERE id = @id AND provider_id = @provider_id AND accepted_at IS NULL;
