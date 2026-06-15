-- +goose Up
-- Vector index for episode semantic search (Phase 2). Deferred from 0016 until
-- embeddings would actually populate. ivfflat with cosine ops matches the
-- "embedding <=> query" search in SearchEpisodes. lists=100 is a sane default
-- for modest row counts; can be rebuilt with more lists as data grows.
CREATE INDEX IF NOT EXISTS idx_user_memory_embedding
    ON user_memory USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 100);

-- +goose Down
DROP INDEX IF EXISTS idx_user_memory_embedding;
