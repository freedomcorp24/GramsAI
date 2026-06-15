-- +goose Up
-- User-created API tokens (multi, revocable) for calling /v1/chat/completions
-- from external code. SEPARATE from users.api_token (the container's internal
-- token, which is never shown nor user-revocable). All billed against the same
-- per-user compute budget — the gateway gate caps spend either way.
CREATE TABLE IF NOT EXISTS api_tokens (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token       TEXT NOT NULL UNIQUE,         -- gsk-...
    name        TEXT NOT NULL DEFAULT '',     -- user label
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used   TIMESTAMPTZ,
    revoked     BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_token ON api_tokens(token) WHERE revoked = false;
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id);

-- +goose Down
DROP TABLE IF EXISTS api_tokens;
