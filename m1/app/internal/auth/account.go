// internal/auth/account.go
// Self-service account endpoints for the settings "Account" tab:
//   GET  /account/info               -> billing/usage/storage snapshot
//   POST /account/password           -> change password (revokes other sessions)
//   GET  /account/sessions           -> list this user's active sessions
//   POST /account/sessions/revoke    -> revoke one session ({token})
//   POST /account/sessions/revoke-others -> revoke all but the current session
// All resolve the caller from the session cookie. Account DELETION lives in the
// router package (it needs the container-purge agent) — see account_delete.go.
package auth

import (
	"encoding/json"
	"net/http"

	"golang.org/x/crypto/bcrypt"
)

// 1 GiB free storage baseline shared by every tier.
const storageBaseBytes int64 = 1 << 30

// resolveCaller returns (uid, currentToken). On failure it writes a 401 and
// returns ok=false so the caller should just return.
func (a *Auth) resolveCaller(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		writeJSON(w, 401, map[string]string{"error": "not authenticated"})
		return 0, "", false
	}
	uid, err := a.Resolve(r.Context(), c.Value)
	if err != nil {
		writeJSON(w, 401, map[string]string{"error": "not authenticated"})
		return 0, "", false
	}
	return uid, c.Value, true
}

// GET /account/info
func (a *Auth) HandleAccountInfo(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	var (
		username, email, tier, status string
		unmetered                     bool
		budgetMicros, usedMicros      int64
		dailyUsed, dailyLimit         int64
		paidUntil                     string
		storageUsed, storageExtra     int64
		pendingTier                   string
		pendingStorage                int64
	)
	err := a.pool.QueryRow(r.Context(), `
		SELECT username, COALESCE(email,''), tier, status, unmetered,
		       compute_budget_micros, compute_used_micros,
		       COALESCE(daily_used_micros,0), COALESCE(daily_limit_micros,0),
		       COALESCE(paid_until::text,''),
		       COALESCE(storage_used_bytes,0), COALESCE(storage_extra_bytes,0),
		       COALESCE(pending_tier,''), COALESCE(pending_storage_bytes,0)
		FROM users WHERE id=$1`, uid).
		Scan(&username, &email, &tier, &status, &unmetered,
			&budgetMicros, &usedMicros,
			&dailyUsed, &dailyLimit,
			&paidUntil,
			&storageUsed, &storageExtra,
			&pendingTier, &pendingStorage)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "lookup failed"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"user_id":               uid,
		"username":              username,
		"email":                 email,
		"tier":                  tier,
		"status":                status,
		"unmetered":             unmetered,
		"budget_micros":         budgetMicros,
		"used_micros":           usedMicros,
		"daily_used_micros":     dailyUsed,
		"daily_limit_micros":    dailyLimit,
		"paid_until":            paidUntil, // "" if never subscribed
		"storage_used_bytes":    storageUsed,
		"storage_extra_bytes":   storageExtra,
		"storage_base_bytes":    storageBaseBytes,
		"storage_quota_bytes":   storageBaseBytes + storageExtra,
		"pending_tier":          pendingTier,          // "" if none
		"pending_storage_bytes": pendingStorage,       // 0 if none
	})
}

// POST /account/password  {old, new}
func (a *Auth) HandleChangePassword(w http.ResponseWriter, r *http.Request) {
	uid, current, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	var body struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	if len(body.New) < 8 {
		writeJSON(w, 400, map[string]string{"error": "new password must be at least 8 characters"})
		return
	}
	var hash string
	if err := a.pool.QueryRow(r.Context(), `SELECT pass_hash FROM users WHERE id=$1`, uid).Scan(&hash); err != nil {
		writeJSON(w, 500, map[string]string{"error": "lookup failed"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Old)) != nil {
		writeJSON(w, 400, map[string]string{"error": "current password is incorrect"})
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(body.New), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "server error"})
		return
	}
	if _, err := a.pool.Exec(r.Context(),
		`UPDATE users SET pass_hash=$2, updated_at=now() WHERE id=$1`, uid, string(newHash)); err != nil {
		writeJSON(w, 500, map[string]string{"error": "server error"})
		return
	}
	// Security: log out every other session, keep the current one.
	_, _ = a.pool.Exec(r.Context(),
		`DELETE FROM sessions WHERE user_id=$1 AND token<>$2`, uid, current)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// GET /account/sessions
func (a *Auth) HandleListSessions(w http.ResponseWriter, r *http.Request) {
	uid, current, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	rows, err := a.pool.Query(r.Context(), `
		SELECT token, COALESCE(user_agent,''), COALESCE(ip,''),
		       created_at::text, last_seen::text, expires_at::text
		FROM sessions
		WHERE user_id=$1 AND expires_at > now()
		ORDER BY last_seen DESC`, uid)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "lookup failed"})
		return
	}
	defer rows.Close()
	out := make([]map[string]any, 0, 8)
	for rows.Next() {
		var token, ua, ip, created, lastSeen, expires string
		if err := rows.Scan(&token, &ua, &ip, &created, &lastSeen, &expires); err != nil {
			writeJSON(w, 500, map[string]string{"error": "lookup failed"})
			return
		}
		out = append(out, map[string]any{
			"token":      token,
			"current":    token == current,
			"user_agent": ua,
			"ip":         ip,
			"created_at": created,
			"last_seen":  lastSeen,
			"expires_at": expires,
		})
	}
	writeJSON(w, 200, map[string]any{"sessions": out})
}

// POST /account/sessions/revoke  {token}
func (a *Auth) HandleRevokeSession(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	// Only ever deletes a session that belongs to this user.
	if _, err := a.pool.Exec(r.Context(),
		`DELETE FROM sessions WHERE user_id=$1 AND token=$2`, uid, body.Token); err != nil {
		writeJSON(w, 500, map[string]string{"error": "server error"})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// POST /account/sessions/revoke-others
func (a *Auth) HandleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	uid, current, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	if _, err := a.pool.Exec(r.Context(),
		`DELETE FROM sessions WHERE user_id=$1 AND token<>$2`, uid, current); err != nil {
		writeJSON(w, 500, map[string]string{"error": "server error"})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
