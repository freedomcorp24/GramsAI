-- +goose Up
-- +goose StatementBegin
ALTER TABLE payments ADD COLUMN IF NOT EXISTS storage_bytes bigint NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE payments DROP COLUMN IF EXISTS storage_bytes;
-- +goose StatementEnd
