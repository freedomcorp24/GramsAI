-- +goose Up
-- Unified per-user memory store. Built once, carries all three phases:
--   Phase 1: mem_type='fact'      (durable user profile, injected always)
--   Phase 2: mem_type='episode'   (past-conversation chunks, searched on demand)
--   Phase 3: mem_type='procedure' (workspace/how-to knowledge)
--
-- Encryption: Option B. content is AES-256-GCM encrypted at the app layer with a key
-- DERIVED FROM THE USER'S PASSWORD at login and held only in memory for the session.
-- The DB stores ciphertext + nonce; the server cannot read it without an active user
-- session. embedding (Phase 2/3) is derived from plaintext at write time while the
-- session key is available, then stored for vector search. NULL in Phase 1.

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS user_memory (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    mem_type        TEXT NOT NULL DEFAULT 'fact',   -- fact | episode | procedure
    category        TEXT NOT NULL DEFAULT 'other',  -- identity|preference|project|relationship|tech_context|other

    -- AES-256-GCM ciphertext of the content + per-row nonce (Option B, user session key)
    content_enc     BYTEA NOT NULL,
    content_nonce   BYTEA NOT NULL,

    -- vector for semantic search (Phase 2/3). 1536 = common OpenRouter embedding dim.
    -- NULL in Phase 1 (facts injected wholesale, no embedding needed).
    embedding       vector(1536),

    confidence      REAL NOT NULL DEFAULT 0.8,
    source_session  TEXT,                            -- which chat session produced it (traceability)

    retrieval_count INT NOT NULL DEFAULT 0,          -- usage score for consolidation
    last_used_at    TIMESTAMPTZ,

    superseded_by   BIGINT REFERENCES user_memory(id) ON DELETE SET NULL, -- update chain; non-null = stale
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- live set = rows not superseded. partial indexes keep reads fast.
CREATE INDEX IF NOT EXISTS idx_user_memory_live
    ON user_memory(user_id)
    WHERE superseded_by IS NULL;

CREATE INDEX IF NOT EXISTS idx_user_memory_type
    ON user_memory(user_id, mem_type)
    WHERE superseded_by IS NULL;

-- NOTE: the ivfflat vector index is intentionally NOT created here. ivfflat clusters
-- based on existing vectors, so it should be built in Phase 2 AFTER embeddings exist.
-- Creating it on an all-NULL column now would be premature and need a rebuild later.

-- per-user memory on/off toggle (the user-facing switch)
ALTER TABLE users ADD COLUMN IF NOT EXISTS memory_enabled BOOLEAN NOT NULL DEFAULT true;

-- extraction watermark: worker only processes messages newer than this per user.
ALTER TABLE users ADD COLUMN IF NOT EXISTS memory_extracted_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS memory_extracted_at;
ALTER TABLE users DROP COLUMN IF EXISTS memory_enabled;
DROP TABLE IF EXISTS user_memory;
-- (vector extension left installed; harmless and may be used elsewhere)
