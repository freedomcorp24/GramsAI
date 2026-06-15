-- +goose Up
-- Per-user gateway API token (containers authenticate with this, NOT the OpenRouter key).
-- Daily spend tracking for per-plan daily limits.
ALTER TABLE users ADD COLUMN IF NOT EXISTS api_token        TEXT UNIQUE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS daily_used_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS daily_limit_cents INTEGER NOT NULL DEFAULT 0; -- 0 = no daily cap
ALTER TABLE users ADD COLUMN IF NOT EXISTS daily_reset      DATE NOT NULL DEFAULT CURRENT_DATE;

CREATE INDEX IF NOT EXISTS idx_users_api_token ON users(api_token);

-- +goose Down
DROP INDEX IF EXISTS idx_users_api_token;
ALTER TABLE users DROP COLUMN IF EXISTS daily_reset;
ALTER TABLE users DROP COLUMN IF EXISTS daily_limit_cents;
ALTER TABLE users DROP COLUMN IF EXISTS daily_used_cents;
ALTER TABLE users DROP COLUMN IF EXISTS api_token;
