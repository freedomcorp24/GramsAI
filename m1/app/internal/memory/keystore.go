// internal/memory/keystore.go
//
// Minimal per-user key store for the encrypted memory/chat feature.
//
// The DEK (data encryption key) that decrypts a user's chats + 3-tier summaries
// lives ONLY here, in Redis (RAM) — never in Postgres next to the encrypted data.
// That separation is what makes the privacy promise real: a Postgres dump/subpoena
// yields ciphertext and no key.
//
// Lifetime: the key follows the user's SESSION. Set on login/unlock, removed on
// logout/expiry. A logged-in user's key stays for the full session life (their
// choice — "logged in = decrypted, log out to seal"). SweepOrphans only removes
// keys whose session no longer exists (crash / missed logout failsafe); it never
// touches a key with a live session.
package memory

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const keyPrefix = "memkey:" // memkey:<user_id> -> base64(DEK)

var ErrNoKey = errors.New("no memory key for user (locked or logged out)")

// KeyStore holds per-user DEKs in Redis, lifetimes tied to sessions.
type KeyStore struct {
	rdb  *redis.Client
	pool *pgxpool.Pool // used by SweepOrphans to check live sessions
	ttl  time.Duration // matches session TTL
}

// NewKeyStore builds a keystore. addr/password come from env (REDIS_ADDR,
// REDIS_PASSWORD). ttl should equal the session TTL. Returns the store and a
// ping error (caller decides whether to treat Redis-down as fatal; we don't —
// memory just stays inactive if Redis is unreachable).
func NewKeyStore(addr, password string, ttl time.Duration, pool *pgxpool.Pool) (*KeyStore, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       0,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := rdb.Ping(ctx).Err()
	return &KeyStore{rdb: rdb, pool: pool, ttl: ttl}, err
}

func redisKey(userID int64) string {
	return keyPrefix + itoa(userID)
}

// Set stores a user's DEK with the session TTL. Called on login/PIN-unlock.
func (k *KeyStore) Set(ctx context.Context, userID int64, dek []byte) error {
	enc := base64.StdEncoding.EncodeToString(dek)
	return k.rdb.Set(ctx, redisKey(userID), enc, k.ttl).Err()
}

// Get returns a user's DEK, or ErrNoKey if absent (locked / logged out).
func (k *KeyStore) Get(ctx context.Context, userID int64) ([]byte, error) {
	enc, err := k.rdb.Get(ctx, redisKey(userID)).Result()
	if err == redis.Nil {
		return nil, ErrNoKey
	}
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(enc)
}

// Del removes a user's DEK. Called on logout, and by the tamper-wipe path.
func (k *KeyStore) Del(ctx context.Context, userID int64) error {
	return k.rdb.Del(ctx, redisKey(userID)).Err()
}

// Has reports whether a key is present (used by the proxy to decide inject-or-skip).
func (k *KeyStore) Has(ctx context.Context, userID int64) bool {
	n, err := k.rdb.Exists(ctx, redisKey(userID)).Result()
	return err == nil && n > 0
}

// SweepOrphans removes keys whose user has NO live session. A logged-in user's
// key is never touched. Run hourly as a failsafe for crashes / missed logouts.
func (k *KeyStore) SweepOrphans(ctx context.Context) (removed int, err error) {
	var cursor uint64
	for {
		var keys []string
		keys, cursor, err = k.rdb.Scan(ctx, cursor, keyPrefix+"*", 200).Result()
		if err != nil {
			return removed, err
		}
		for _, rk := range keys {
			uidStr := rk[len(keyPrefix):]
			uid := atoi(uidStr)
			if uid == 0 {
				continue
			}
			// does this user still have at least one unexpired session?
			var live bool
			qerr := k.pool.QueryRow(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM sessions
					WHERE user_id=$1 AND expires_at > now()
				)`, uid).Scan(&live)
			if qerr != nil {
				continue // be conservative: on error, leave the key
			}
			if !live {
				if k.rdb.Del(ctx, rk).Err() == nil {
					removed++
				}
			}
		}
		if cursor == 0 {
			break
		}
	}
	return removed, nil
}

// StartSweeper runs SweepOrphans on an interval (mirrors the Start* reaper pattern).
func (k *KeyStore) StartSweeper(every time.Duration) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, _ = k.SweepOrphans(ctx)
			cancel()
		}
	}()
}

// --- tiny int<->string helpers (avoid strconv import churn / keep deps minimal) ---

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func atoi(s string) int64 {
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
