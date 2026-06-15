-- +goose Up
-- Payment-gate column. access = status='active' AND (unmetered OR paid_until > now()).
-- NULL paid_until = never paid (no access unless unmetered).
ALTER TABLE users ADD COLUMN IF NOT EXISTS paid_until timestamptz;

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS paid_until;
