-- +goose Up
-- Track per-conversation (opencode session) episode state for change detection.
-- session_id = opencode's ses_xxx. msg_count = messages summarized last time.
-- memory_id  = the user_memory episode row (so we can supersede on update).
CREATE TABLE IF NOT EXISTS session_episodes (
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id TEXT   NOT NULL,
    memory_id  BIGINT,
    msg_count  INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, session_id)
);
CREATE INDEX IF NOT EXISTS idx_session_episodes_user ON session_episodes(user_id);

-- +goose Down
DROP TABLE IF EXISTS session_episodes;
