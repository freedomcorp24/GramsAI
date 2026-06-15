-- +goose Up
-- TOTP 2FA: per-user secret + enabled flag + single-use recovery codes,
-- plus a short-lived "password OK, awaiting code" challenge table.

-- base32 TOTP secret (null until the user starts setup; kept even while
-- disabled so a half-finished setup can be resumed/confirmed).
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret    TEXT;

-- only true once the user has confirmed a code during setup.
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled   BOOLEAN NOT NULL DEFAULT false;

-- bcrypt-hashed, single-use recovery codes. Empty array = none issued.
ALTER TABLE users ADD COLUMN IF NOT EXISTS recovery_codes TEXT[] NOT NULL DEFAULT '{}';

-- Interim challenge: created after a correct password when totp_enabled=true.
-- The real session cookie is only issued after the code/recovery is verified.
-- Short TTL; revocable; auto-expiring. Mirrors the sessions table choice
-- (server-side, revocable) over a stateless token.
CREATE TABLE IF NOT EXISTS totp_pending (
    token       TEXT PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    user_agent  TEXT,
    ip          TEXT
);
CREATE INDEX IF NOT EXISTS idx_totp_pending_expires ON totp_pending(expires_at);

-- +goose Down
DROP TABLE IF EXISTS totp_pending;
ALTER TABLE users DROP COLUMN IF EXISTS recovery_codes;
ALTER TABLE users DROP COLUMN IF EXISTS totp_enabled;
ALTER TABLE users DROP COLUMN IF EXISTS totp_secret;
