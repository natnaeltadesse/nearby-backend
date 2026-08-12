-- name: AddMember :one
INSERT INTO members (provider_id, user_id, role)
VALUES (@provider_id, @user_id, @role)
ON CONFLICT (provider_id, user_id) DO UPDATE SET role = EXCLUDED.role
RETURNING provider_id, user_id, role, created_at;

-- The membership check behind every /org/* request. `x-organization-id` is
-- client-supplied, so this runs before any org-scoped data is read.
-- name: GetMembership :one
SELECT provider_id, user_id, role, created_at
FROM members
WHERE provider_id = @provider_id AND user_id = @user_id;

-- Feeds the JWT `memberships` claim at sign-in and refresh.
-- name: ListMembershipsByUser :many
SELECT m.provider_id, m.role, p.slug, p.name, p.status
FROM members m
JOIN providers p ON p.id = m.provider_id
WHERE m.user_id = @user_id
ORDER BY p.name;

-- name: ListMembers :many
SELECT m.provider_id, m.user_id, m.role, m.created_at,
       u.name, u.email, u.phone
FROM members m
JOIN users u ON u.id = m.user_id
WHERE m.provider_id = @provider_id
ORDER BY
    CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END,
    u.name;

-- name: UpdateMemberRole :one
UPDATE members
SET role = @role
WHERE provider_id = @provider_id AND user_id = @user_id
RETURNING provider_id, user_id, role, created_at;

-- name: RemoveMember :execrows
DELETE FROM members
WHERE provider_id = @provider_id AND user_id = @user_id;

-- Guards against removing or demoting the last owner of a provider.
-- name: CountOwners :one
SELECT count(*) FROM members
WHERE provider_id = @provider_id AND role = 'owner';
