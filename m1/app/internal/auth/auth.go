// internal/auth/auth.go
// Frontend authentication: username+password signup, bcrypt, server-side
// sessions (revocable). Suspended accounts are rejected at login and on every
// session resolve. This is the identity layer the per-user routing sits on.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"gramsai/internal/memory"
)

const (
	CookieName = "gramsai_session"
	sessionTTL = 30 * 24 * time.Hour // 30 days
)

var (
	ErrTaken        = errors.New("username taken")
	ErrInvalid      = errors.New("invalid username or password")
	ErrSuspended    = errors.New("account suspended")
	ErrBadUsername  = errors.New("username must be 3-32 chars, alphanumeric/_-")
	ErrWeakPassword = errors.New("password must be at least 8 characters")
)

type ctxKey int

const userIDKey ctxKey = 1

// UserID pulls the authenticated user id attached by Middleware. 0 if absent.
func UserID(ctx context.Context) int64 {
	if v, ok := ctx.Value(userIDKey).(int64); ok {
		return v
	}
	return 0
}

// GRAMSAI_DEK_STRUCT
type Auth struct {
	pool *pgxpool.Pool
	keys *memory.KeyStore // per-user DEK store (Redis); may be nil if Redis down
}

func New(pool *pgxpool.Pool, keys *memory.KeyStore) *Auth { return &Auth{pool: pool, keys: keys} }

// Register creates a username+password account, mints its gsk- api token,
// and seeds budget from tier_defaults for 'basic' (or built-in fallback).
func (a *Auth) Register(ctx context.Context, username, password string) (int64, error) {
	username = strings.TrimSpace(username)
	if !validUsername(username) {
		return 0, ErrBadUsername
	}
	if len(password) < 8 {
		return 0, ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	apiToken := "gsk-" + randHex(32)

	// budget default for basic tier (micros). Fallback $10 if tier_defaults absent.
	var budgetMicros, dailyMicros int64 = 10_000_000, 0
	_ = a.pool.QueryRow(ctx,
		`SELECT budget_micros, daily_limit_micros FROM tier_defaults WHERE tier='basic'`).
		Scan(&budgetMicros, &dailyMicros)

	budgetCents := budgetMicros / 10000
	dailyCents := dailyMicros / 10000
	// GRAMSAI_DEK_REGISTER: provision the per-user DEK, wrapped by the password.
	// Best-effort: if provisioning fails, the account is still created (DEK can be
	// provisioned lazily on a later login).
	encDEK, dekSalt, derr := memory.ProvisionDEK(password)
	if derr != nil {
		encDEK, dekSalt = nil, nil
	}
	var id int64
	err = a.pool.QueryRow(ctx, `
		INSERT INTO users (username, pass_hash, tier, status, unmetered,
		                   api_token, compute_budget_micros, daily_limit_micros,
		                   compute_budget_cents, daily_limit_cents, enc_dek, dek_salt)
		VALUES ($1,$2,'basic','active',false,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id`,
		username, string(hash), apiToken, budgetMicros, dailyMicros, budgetCents, dailyCents, encDEK, dekSalt).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "users_username_key") || strings.Contains(err.Error(), "duplicate") {
			return 0, ErrTaken
		}
		return 0, err
	}
	return id, nil
}

// GRAMSAI_DEK_UNLOCK: unlock the user's DEK with the password and push it to
// Redis for the session. If the account has no DEK yet (old account), provision
// one now (password is available). All best-effort: never blocks login.
func (a *Auth) unlockDEKToRedis(ctx context.Context, uid int64, password string) {
	if a.keys == nil {
		return
	}
	var encDEK, dekSalt []byte
	err := a.pool.QueryRow(ctx, `SELECT enc_dek, dek_salt FROM users WHERE id=$1`, uid).Scan(&encDEK, &dekSalt)
	if err != nil {
		return
	}
	if len(encDEK) == 0 || len(dekSalt) == 0 {
		// lazily provision for accounts created before encryption existed
		w, s, perr := memory.ProvisionDEK(password)
		if perr != nil {
			return
		}
		if _, uerr := a.pool.Exec(ctx, `UPDATE users SET enc_dek=$2, dek_salt=$3 WHERE id=$1`, uid, w, s); uerr != nil {
			return
		}
		encDEK, dekSalt = w, s
	}
	dek, derr := memory.UnlockDEK(password, encDEK, dekSalt)
	if derr != nil {
		return
	}
	_ = a.keys.Set(ctx, uid, dek)
}

// Login verifies credentials and creates a session. Rejects suspended accounts.
func (a *Auth) Login(ctx context.Context, username, password, ua, ip string) (string, error) {
	username = strings.TrimSpace(username)
	var (
		id     int64
		hash   string
		status string
	)
	err := a.pool.QueryRow(ctx,
		`SELECT id, pass_hash, status FROM users WHERE username=$1`, username).
		Scan(&id, &hash, &status)
	if err == pgx.ErrNoRows {
		// run a dummy compare to keep timing uniform against user enumeration
		bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinv"), []byte(password))
		return "", ErrInvalid
	}
	if err != nil {
		return "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", ErrInvalid
	}
	if status == "suspended" {
		return "", ErrSuspended
	}
	a.unlockDEKToRedis(ctx, id, password) // GRAMSAI_DEK_LOGIN
	return a.issueSession(ctx, id, ua, ip)
}

// issueSession creates a server-side session row and returns its token.
// Shared by the normal login path and the post-2FA challenge path.
func (a *Auth) issueSession(ctx context.Context, uid int64, ua, ip string) (string, error) {
	token := randHex(32)
	_, err := a.pool.Exec(ctx, `
		INSERT INTO sessions (token, user_id, expires_at, user_agent, ip)
		VALUES ($1,$2,$3,$4,$5)`,
		token, uid, time.Now().Add(sessionTTL), trunc(ua, 300), trunc(ip, 64))
	if err != nil {
		return "", err
	}
	return token, nil
}

// LoginStep1 verifies the password and reports whether a 2FA challenge is
// required. If twoFA is false, a session token is returned (logged in). If
// twoFA is true, token is empty and uid identifies the account to challenge.
func (a *Auth) LoginStep1(ctx context.Context, username, password, ua, ip string) (token string, twoFA bool, uid int64, err error) {
	username = strings.TrimSpace(username)
	var (
		id       int64
		hash     string
		status   string
		enabled  bool
	)
	qerr := a.pool.QueryRow(ctx,
		`SELECT id, pass_hash, status, totp_enabled FROM users WHERE username=$1`, username).
		Scan(&id, &hash, &status, &enabled)
	if qerr == pgx.ErrNoRows {
		bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinv"), []byte(password))
		return "", false, 0, ErrInvalid
	}
	if qerr != nil {
		return "", false, 0, qerr
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", false, 0, ErrInvalid
	}
	if status == "suspended" {
		return "", false, 0, ErrSuspended
	}
	if enabled {
		return "", true, id, nil
	}
	a.unlockDEKToRedis(ctx, id, password) // GRAMSAI_DEK_STEP1
	tok, serr := a.issueSession(ctx, id, ua, ip)
	if serr != nil {
		return "", false, 0, serr
	}
	return tok, false, id, nil
}

func (a *Auth) Logout(ctx context.Context, token string) {
	if token == "" {
		return
	}
	// GRAMSAI_DEK_LOGOUT: drop the user's DEK from Redis along with the session.
	if a.keys != nil {
		var uid int64
		if a.pool.QueryRow(ctx, `SELECT user_id FROM sessions WHERE token=$1`, token).Scan(&uid) == nil {
			_ = a.keys.Del(ctx, uid)
		}
	}
	_, _ = a.pool.Exec(ctx, `DELETE FROM sessions WHERE token=$1`, token)
}

// Resolve maps a session token to a user id, enforcing expiry and suspension.
func (a *Auth) Resolve(ctx context.Context, token string) (int64, error) {
	if token == "" {
		return 0, ErrInvalid
	}
	var (
		uid     int64
		expires time.Time
		status  string
	)
	err := a.pool.QueryRow(ctx, `
		SELECT s.user_id, s.expires_at, u.status
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token=$1`, token).Scan(&uid, &expires, &status)
	if err != nil {
		return 0, ErrInvalid
	}
	if time.Now().After(expires) {
		_, _ = a.pool.Exec(ctx, `DELETE FROM sessions WHERE token=$1`, token)
		return 0, ErrInvalid
	}
	if status == "suspended" {
		return 0, ErrSuspended
	}
	// best-effort last_seen bump (non-blocking correctness)
	_, _ = a.pool.Exec(ctx, `UPDATE sessions SET last_seen=now() WHERE token=$1`, token)
	return uid, nil
}

// Middleware attaches the user id to context; on failure it calls onFail
// (e.g. redirect to /login for the gatekeeper, or 401 for API routes).
func (a *Auth) Middleware(onFail http.HandlerFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(CookieName)
			if err != nil {
				onFail(w, r)
				return
			}
			uid, err := a.Resolve(r.Context(), c.Value)
			if err != nil {
				onFail(w, r)
				return
			}
			ctx := context.WithValue(r.Context(), userIDKey, uid)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func SetCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionTTL.Seconds()),
		// Secure omitted: served over Tor/onion (http) and internal. Add Secure
		// if/when fronted by TLS on clearnet.
	})
}

func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}

// ---- helpers ----

func validUsername(s string) bool {
	if len(s) < 3 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
