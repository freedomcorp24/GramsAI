-- +goose Up
-- +goose StatementBegin
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS storage_used_bytes  bigint      NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS storage_extra_bytes bigint      NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS storage_checked_at  timestamptz;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE tier_defaults
  ADD COLUMN IF NOT EXISTS storage_bytes bigint NOT NULL DEFAULT 1073741824;
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE tier_defaults SET storage_bytes = 1073741824   WHERE tier = 'basic';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE tier_defaults SET storage_bytes = 5368709120   WHERE tier = 'pro';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE tier_defaults SET storage_bytes = 26843545600  WHERE tier = 'max';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE tier_defaults SET storage_bytes = 107374182400 WHERE tier = 'ultra';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users         DROP COLUMN IF EXISTS storage_used_bytes;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE users         DROP COLUMN IF EXISTS storage_extra_bytes;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE users         DROP COLUMN IF EXISTS storage_checked_at;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE tier_defaults DROP COLUMN IF EXISTS storage_bytes;
-- +goose StatementEnd
