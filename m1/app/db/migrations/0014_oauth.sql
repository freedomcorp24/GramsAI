-- +goose Up
-- Social login identities. One row per (provider, provider_uid); links to a
-- users row. A user MAY have multiple linked providers. Email is captured from
-- the provider for display only (never used to send mail).
CREATE TABLE IF NOT EXISTS oauth_identities (
    provider      TEXT   NOT NULL,            -- 'github' | 'google'
    provider_uid  TEXT   NOT NULL,            -- the provider's stable user id
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email         TEXT,                       -- captured, display-only
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, provider_uid)
);
CREATE INDEX IF NOT EXISTS idx_oauth_user ON oauth_identities(user_id);

-- short-lived signed-state isn't enough alone; we also keep a server-side state
-- row so a callback can't be replayed and we can carry intent (login vs link).
CREATE TABLE IF NOT EXISTS oauth_state (
    state       TEXT PRIMARY KEY,
    provider    TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_oauth_state_expires ON oauth_state(expires_at);

-- +goose Down
DROP TABLE IF EXISTS oauth_state;
DROP TABLE IF EXISTS oauth_identities;
