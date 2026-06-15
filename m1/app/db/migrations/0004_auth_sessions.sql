-- +goose Up
-- Frontend auth: sessions + account status + unmetered flag.

-- Account lifecycle: active | suspended. Suspended = login, container, and
-- LLM calls all rejected. Checked at login and on every metered request.
ALTER TABLE users ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';

-- Unmetered (unlimited) accounts: gateway skips budget/daily checks entirely.
-- Usage is still logged for visibility; it just never blocks.
ALTER TABLE users ADD COLUMN IF NOT EXISTS unmetered BOOLEAN NOT NULL DEFAULT false;

-- Server-side sessions (revocable, the secure choice over stateless JWT).
CREATE TABLE IF NOT EXISTS sessions (
    token       TEXT PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_agent  TEXT,
    ip          TEXT
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

-- +goose Down
DROP TABLE IF EXISTS sessions;
ALTER TABLE users DROP COLUMN IF EXISTS unmetered;
ALTER TABLE users DROP COLUMN IF EXISTS status;
