-- +goose Up

-- The cover is the wide banner behind a provider's header; the logo (already
-- on `providers` since 00003) is the small square mark. They are separate
-- pictures with separate crops, so one column cannot serve both.
--
-- Both carry a public_id alongside the URL for the same reason service_media
-- does: the key is what lets the bytes be removed when the picture is replaced.
ALTER TABLE providers
    ADD COLUMN cover_url       text,
    ADD COLUMN cover_public_id text;

-- +goose Down
ALTER TABLE providers
    DROP COLUMN IF EXISTS cover_public_id,
    DROP COLUMN IF EXISTS cover_url;
