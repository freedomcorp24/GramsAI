-- +goose Up
-- +goose StatementBegin
ALTER TABLE containers ADD COLUMN IF NOT EXISTS browser_port integer;
-- +goose StatementEnd
-- +goose StatementBegin
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'containers_host_id_browser_port_key') THEN
    ALTER TABLE containers ADD CONSTRAINT containers_host_id_browser_port_key UNIQUE (host_id, browser_port);
  END IF;
END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
ALTER TABLE containers DROP CONSTRAINT IF EXISTS containers_host_id_browser_port_key;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE containers DROP COLUMN IF EXISTS browser_port;
-- +goose StatementEnd
