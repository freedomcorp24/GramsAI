// internal/auth/reset.go
//
// Password reset via 2FA recovery code. The ONLY reset path (no email/SMTP):
//   - 2FA users reset with a recovery code (consumed, single-use).
//   - OAuth users don't reset — they just log in socially.
//   - Users with neither cannot reset (by design).
package auth

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// POST /auth/reset  {username, recovery_code, new_password}
// Verifies a single-use recovery code and sets a new password. Generic errors
// avoid leaking whether a username exists or has 2FA configured.
func (a *Auth) HandleResetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username     string `json:"username"`
		RecoveryCode string `json:"recovery_code"`
		NewPassword  string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if body.Username == "" || body.RecoveryCode == "" {
		writeJSON(w, 400, map[string]string{"error": "username and recovery code required"})
		return
	}
	if len(body.NewPassword) < 8 {
		writeJSON(w, 400, map[string]string{"error": "new password must be at least 8 characters"})
		return
	}

	var (
		uid     int64
		enabled bool
		status  string
	)
	err := a.pool.QueryRow(r.Context(),
		`SELECT id, totp_enabled, status FROM users WHERE username=$1`, body.Username).
		Scan(&uid, &enabled, &status)
	if err == pgx.ErrNoRows {
		writeJSON(w, 401, map[string]string{"error": "invalid username or recovery code"})
		return
	}
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "lookup failed"})
		return
	}
	if status == "suspended" {
		writeJSON(w, 403, map[string]string{"error": "account suspended"})
		return
	}
	if !enabled {
		// No 2FA => no recovery path. Same generic message (no info leak).
		writeJSON(w, 401, map[string]string{"error": "invalid username or recovery code"})
		return
	}
	if !a.useRecoveryCode(r.Context(), uid, body.RecoveryCode) {
		writeJSON(w, 401, map[string]string{"error": "invalid username or recovery code"})
		return
	}

	newHash, herr := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
	if herr != nil {
		writeJSON(w, 500, map[string]string{"error": "server error"})
		return
	}
	if _, err := a.pool.Exec(r.Context(),
		`UPDATE users SET pass_hash=$2, updated_at=now() WHERE id=$1`, uid, string(newHash)); err != nil {
		writeJSON(w, 500, map[string]string{"error": "server error"})
		return
	}
	// A reset invalidates ALL sessions + any in-flight 2FA challenges.
	_, _ = a.pool.Exec(r.Context(), `DELETE FROM sessions WHERE user_id=$1`, uid)
	_, _ = a.pool.Exec(r.Context(), `DELETE FROM totp_pending WHERE user_id=$1`, uid)

	writeJSON(w, 200, map[string]any{"ok": true})
}
