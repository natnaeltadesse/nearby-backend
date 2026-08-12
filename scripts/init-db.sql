-- Runs once, on first boot of an empty booking_pgdata volume.
-- Everything after this point is goose's job; this file only guarantees the
-- extensions the schema depends on exist before the first migration runs.

-- Nearby-provider search (discovery).
CREATE EXTENSION IF NOT EXISTS postgis;

-- Double-booking prevention: lets a GiST exclusion constraint mix the
-- equality operator on resource_id with the overlap operator on tstzrange.
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- gen_random_uuid() also ships with pgcrypto/core in PG16, but the spec
-- calls for uuid-ossp explicitly.
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
