-- +goose Up
-- scripts/init-db.sql already does this for the docker-compose database, but
-- migrations must be self-sufficient: integration tests spin up a bare
-- container and run goose against it with no init script.

-- nearby-provider search
CREATE EXTENSION IF NOT EXISTS postgis;
-- lets a GiST exclusion constraint mix `=` on resource_id with `&&` on tstzrange
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- +goose Down
-- Intentionally not dropped: other databases in the cluster may share them,
-- and dropping postgis would cascade into every geography column.
SELECT 1;
