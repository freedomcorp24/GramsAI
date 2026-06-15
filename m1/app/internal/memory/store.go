// internal/memory/store.go
//
// Tier-agnostic encrypted memory store. Serves all three tiers (fact / episode /
// procedure) — they differ only in mem_type, not in storage or crypto.
//
// Write path (StoreMemory): encrypt plaintext with the user's DEK, INSERT one row.
//   Used by the extraction worker (1e). Infrequent (<=1 run/user/24h).
// Read path (LoadMemories):  one indexed query for all live rows of a type,
//   decrypt each. On the HOT path (prompt injection) we serve from a Redis cache
//   so we don't hit Postgres + decrypt on every message — cache is invalidated on
//   write and is session-tied (cleared with the DEK on logout/sweep).
//
// Combined-blob crypto: EncryptContent returns nonce||ciphertext as one blob,
// stored whole in content_enc. content_nonce is unused (kept for schema-compat).
package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Memory is one decrypted memory row (any tier).
type Memory struct {
	ID         int64   `json:"id"`
	Type       string  `json:"type"`     // fact | episode | procedure
	Category   string  `json:"category"` // identity | preference | ...
	Content    string  `json:"content"`  // decrypted plaintext
	Confidence float32 `json:"confidence"`
	CreatedAt  time.Time `json:"created_at"`
}

const factCachePrefix = "memcache:" // memcache:<user_id>:<type> -> JSON []Memory

// Store is the memory persistence + cache layer. It depends on the KeyStore for
// per-user DEKs and a Redis client for the decrypted-fact cache.
type Store struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	keys *KeyStore
}

func NewStore(pool *pgxpool.Pool, keys *KeyStore) *Store {
	return &Store{pool: pool, rdb: keys.rdb, keys: keys}
}

// StoreMemory encrypts and inserts one memory row for a user. The DEK must be in
// Redis (active session); returns ErrNoKey if not. Invalidates the read cache.
func (s *Store) StoreMemory(ctx context.Context, userID int64, memType, category, content string, confidence float32, sourceSession string) error {
	dek, err := s.keys.Get(ctx, userID)
	if err != nil {
		return err // ErrNoKey if locked/logged out
	}
	blob, err := EncryptContent(dek, []byte(content))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO user_memory (user_id, mem_type, category, content_enc, content_nonce, confidence, source_session)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		userID, memType, category, blob, []byte{}, confidence, sourceSession)
	if err != nil {
		return err
	}
	s.invalidate(ctx, userID, memType)
	return nil
}

// SupersedeMemory marks an old row stale and inserts the replacement (UPDATE path
// for conflict resolution). Both in one txn. Invalidates cache.
func (s *Store) SupersedeMemory(ctx context.Context, userID, oldID int64, memType, category, content string, confidence float32, sourceSession string) error {
	dek, err := s.keys.Get(ctx, userID)
	if err != nil {
		return err
	}
	blob, err := EncryptContent(dek, []byte(content))
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var newID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO user_memory (user_id, mem_type, category, content_enc, content_nonce, confidence, source_session)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		userID, memType, category, blob, []byte{}, confidence, sourceSession).Scan(&newID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE user_memory SET superseded_by=$2, updated_at=now() WHERE id=$1 AND user_id=$3`,
		oldID, newID, userID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.invalidate(ctx, userID, memType)
	return nil
}

// DeleteMemory hard-deletes a row (user CRUD). Invalidates cache.
func (s *Store) DeleteMemory(ctx context.Context, userID, id int64, memType string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM user_memory WHERE id=$1 AND user_id=$2`, id, userID)
	if err == nil {
		s.invalidate(ctx, userID, memType)
	}
	return err
}

// LoadMemories returns all live (non-superseded) memories of a type for a user,
// decrypted. Serves the hot path via a Redis cache; on miss it reads Postgres,
// decrypts, and populates the cache. Returns ErrNoKey if the DEK isn't available.
func (s *Store) LoadMemories(ctx context.Context, userID int64, memType string) ([]Memory, error) {
	// cache hit?
	if cached, ok := s.cacheGet(ctx, userID, memType); ok {
		return cached, nil
	}
	dek, err := s.keys.Get(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, mem_type, category, content_enc, confidence, created_at
		FROM user_memory
		WHERE user_id=$1 AND mem_type=$2 AND superseded_by IS NULL
		ORDER BY confidence DESC, created_at DESC`, userID, memType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Memory
	for rows.Next() {
		var m Memory
		var blob []byte
		if err := rows.Scan(&m.ID, &m.Type, &m.Category, &blob, &m.Confidence, &m.CreatedAt); err != nil {
			return nil, err
		}
		pt, derr := DecryptContent(dek, blob)
		if derr != nil {
			continue // skip undecryptable rows rather than fail the whole load
		}
		m.Content = string(pt)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.cacheSet(ctx, userID, memType, out)
	return out, nil
}

// CountLive returns how many live memories of a type a user has (for the cap).
func (s *Store) CountLive(ctx context.Context, userID int64, memType string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM user_memory
		WHERE user_id=$1 AND mem_type=$2 AND superseded_by IS NULL`, userID, memType).Scan(&n)
	return n, err
}

// --- Redis cache of decrypted memories (hot-path optimization) ---

func cacheKey(userID int64, memType string) string {
	return factCachePrefix + itoa(userID) + ":" + memType
}

func (s *Store) cacheGet(ctx context.Context, userID int64, memType string) ([]Memory, bool) {
	if s.rdb == nil {
		return nil, false
	}
	v, err := s.rdb.Get(ctx, cacheKey(userID, memType)).Result()
	if err != nil {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, false
	}
	var ms []Memory
	if json.Unmarshal(raw, &ms) != nil {
		return nil, false
	}
	return ms, true
}

func (s *Store) cacheSet(ctx context.Context, userID int64, memType string, ms []Memory) {
	if s.rdb == nil {
		return
	}
	raw, err := json.Marshal(ms)
	if err != nil {
		return
	}
	enc := base64.StdEncoding.EncodeToString(raw)
	// TTL ties cache to roughly session life; invalidated explicitly on write.
	_ = s.rdb.Set(ctx, cacheKey(userID, memType), enc, 30*24*time.Hour).Err()
}

func (s *Store) invalidate(ctx context.Context, userID int64, memType string) {
	if s.rdb == nil {
		return
	}
	_ = s.rdb.Del(ctx, cacheKey(userID, memType)).Err()
}
