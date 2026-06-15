// internal/auth/twofa_handlers.go
//
// 2FA HTTP endpoints:
//   POST /account/2fa/setup    -> start setup: returns secret + otpauth URI (QR)
//   POST /account/2fa/enable   -> confirm a code, flip enabled, return recovery codes (once)
//   POST /account/2fa/disable  -> verify code/recovery, clear secret + codes
//   GET  /account/2fa/status   -> {enabled, recovery_remaining}
//   POST /auth/login/totp      -> the login challenge: {pending, code} -> session cookie
//
// Login itself is handled by HandleLogin (handlers.go); when the account has
// totp_enabled, HandleLogin issues a short-lived pending token instead of a
// session and the client finishes here.
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// ---- setup: generate (or re-generate) a secret, store it, return QR data ----
// POST /account/2fa/setup   (auth required)
func (a *Auth) HandleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	// Don't let an already-enabled account silently re-key; require disable first.
	var enabled bool
	var username string
	if err := a.pool.QueryRow(r.Context(),
		`SELECT totp_enabled, username FROM users WHERE id=$1`, uid).
		Scan(&enabled, &username); err != nil {
		writeJSON(w, 500, map[string]string{"error": "lookup failed"})
		return
	}
	if enabled {
		writeJSON(w, 409, map[string]string{"error": "2fa already enabled; disable it first"})
		return
	}
	secret, err := newTOTPSecret()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not generate secret"})
		return
	}
	if _, err := a.pool.Exec(r.Context(),
		`UPDATE users SET totp_secret=$2, totp_enabled=false WHERE id=$1`, uid, secret); err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not store secret"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"secret":  secret,
		"otpauth": otpauthURI(secret, username),
	})
}

// ---- enable: confirm a code against the pending secret, issue recovery codes --
// POST /account/2fa/enable  {code}   (auth required)
func (a *Auth) HandleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	var body struct{ Code string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	var secret string
	var enabled bool
	if err := a.pool.QueryRow(r.Context(),
		`SELECT COALESCE(totp_secret,''), totp_enabled FROM users WHERE id=$1`, uid).
		Scan(&secret, &enabled); err != nil {
		writeJSON(w, 500, map[string]string{"error": "lookup failed"})
		return
	}
	if enabled {
		writeJSON(w, 409, map[string]string{"error": "2fa already enabled"})
		return
	}
	if secret == "" {
		writeJSON(w, 400, map[string]string{"error": "run setup first"})
		return
	}
	if !verifyTOTP(secret, body.Code) {
		writeJSON(w, 401, map[string]string{"error": "invalid code"})
		return
	}
	plain, hashed, err := genRecoveryCodes()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not generate recovery codes"})
		return
	}
	if _, err := a.pool.Exec(r.Context(),
		`UPDATE users SET totp_enabled=true, recovery_codes=$2 WHERE id=$1`, uid, hashed); err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not enable"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":             true,
		"recovery_codes": plain, // shown ONCE
	})
}

// ---- disable: verify a code OR recovery code, then clear everything ----
// POST /account/2fa/disable  {code}   (auth required)
func (a *Auth) HandleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	var body struct{ Code string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	var secret string
	var enabled bool
	if err := a.pool.QueryRow(r.Context(),
		`SELECT COALESCE(totp_secret,''), totp_enabled FROM users WHERE id=$1`, uid).
		Scan(&secret, &enabled); err != nil {
		writeJSON(w, 500, map[string]string{"error": "lookup failed"})
		return
	}
	if !enabled {
		writeJSON(w, 400, map[string]string{"error": "2fa not enabled"})
		return
	}
	if !verifyTOTP(secret, body.Code) && !a.useRecoveryCode(r.Context(), uid, body.Code) {
		writeJSON(w, 401, map[string]string{"error": "invalid code"})
		return
	}
	if _, err := a.pool.Exec(r.Context(),
		`UPDATE users SET totp_enabled=false, totp_secret=NULL, recovery_codes='{}' WHERE id=$1`, uid); err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not disable"})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- status: for the account UI ----
// GET /account/2fa/status   (auth required)
func (a *Auth) HandleTOTPStatus(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	var enabled bool
	var codes []string
	if err := a.pool.QueryRow(r.Context(),
		`SELECT totp_enabled, recovery_codes FROM users WHERE id=$1`, uid).
		Scan(&enabled, &codes); err != nil {
		writeJSON(w, 500, map[string]string{"error": "lookup failed"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"enabled":            enabled,
		"recovery_remaining": len(codes),
	})
}

// ---- login challenge: exchange a pending token + code for a real session ----
// POST /auth/login/totp  {pending, code}
func (a *Auth) HandleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	var body struct{ Pending, Code string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	if body.Pending == "" {
		writeJSON(w, 400, map[string]string{"error": "missing challenge"})
		return
	}
	// Resolve the pending challenge (and expire it).
	var uid int64
	var expires time.Time
	err := a.pool.QueryRow(r.Context(),
		`SELECT user_id, expires_at FROM totp_pending WHERE token=$1`, body.Pending).
		Scan(&uid, &expires)
	if err != nil {
		writeJSON(w, 401, map[string]string{"error": "challenge expired"})
		return
	}
	if time.Now().After(expires) {
		_, _ = a.pool.Exec(r.Context(), `DELETE FROM totp_pending WHERE token=$1`, body.Pending)
		writeJSON(w, 401, map[string]string{"error": "challenge expired"})
		return
	}
	var secret string
	var status string
	if err := a.pool.QueryRow(r.Context(),
		`SELECT COALESCE(totp_secret,''), status FROM users WHERE id=$1`, uid).
		Scan(&secret, &status); err != nil {
		writeJSON(w, 500, map[string]string{"error": "lookup failed"})
		return
	}
	if status == "suspended" {
		writeJSON(w, 403, map[string]string{"error": "account suspended"})
		return
	}
	// Accept a TOTP code OR a single-use recovery code.
	if !verifyTOTP(secret, body.Code) && !a.useRecoveryCode(r.Context(), uid, body.Code) {
		writeJSON(w, 401, map[string]string{"error": "invalid code"})
		return
	}
	// Success: consume the challenge, mint the real session.
	_, _ = a.pool.Exec(r.Context(), `DELETE FROM totp_pending WHERE token=$1`, body.Pending)
	token, err := a.issueSession(r.Context(), uid, r.UserAgent(), clientIP(r))
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not create session"})
		return
	}
	SetCookie(w, token)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// newPendingChallenge stores a short-lived challenge after a correct password.
func (a *Auth) newPendingChallenge(ctx context.Context, uid int64, ua, ip string) (string, error) {
	token := randHex(32)
	_, err := a.pool.Exec(ctx, `
		INSERT INTO totp_pending (token, user_id, expires_at, user_agent, ip)
		VALUES ($1,$2,$3,$4,$5)`,
		token, uid, time.Now().Add(pendingTTL), trunc(ua, 300), trunc(ip, 64))
	return token, err
}
