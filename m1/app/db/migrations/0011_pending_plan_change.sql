-- +goose Up
-- +goose StatementBegin
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS pending_tier          text,
  ADD COLUMN IF NOT EXISTS pending_storage_bytes bigint;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN IF EXISTS pending_tier;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN IF EXISTS pending_storage_bytes;
-- +goose StatementEnd
