// internal/auth/tokens.go
//
// User-created API tokens (multi, revocable). These call /v1/chat/completions
// and bill against the same per-user budget. Separate from users.api_token (the
// container's internal token, never exposed here).
package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

type apiToken struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Masked   string `json:"masked"`    // gsk-…last4, never the full token
	Created  string `json:"created"`
	LastUsed string `json:"last_used"` // "" if never used
}

func maskToken(t string) string {
	if len(t) <= 8 {
		return "gsk-…"
	}
	return t[:7] + "…" + t[len(t)-4:]
}

// GET /account/tokens -> {tokens:[{id,name,masked,created,last_used}]}
func (a *Auth) HandleListTokens(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	rows, err := a.pool.Query(r.Context(), `
		SELECT id, name, token, created_at::text, COALESCE(last_used::text,'')
		FROM api_tokens
		WHERE user_id=$1 AND revoked=false
		ORDER BY created_at DESC`, uid)
	out := []apiToken{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var t apiToken
			var full string
			if rows.Scan(&t.ID, &t.Name, &full, &t.Created, &t.LastUsed) == nil {
				t.Masked = maskToken(full)
				out = append(out, t)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"tokens": out})
}

// POST /account/tokens {name} -> {id, name, token}  (full token shown ONCE)
func (a *Auth) HandleCreateToken(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	var body struct{ Name string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "API token"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	// cap per user to a sane number.
	var n int
	_ = a.pool.QueryRow(r.Context(),
		`SELECT count(*) FROM api_tokens WHERE user_id=$1 AND revoked=false`, uid).Scan(&n)
	if n >= 20 {
		writeJSON(w, 409, map[string]string{"error": "token limit reached (revoke some first)"})
		return
	}
	token := "gsk-" + randHex(32)
	var id int64
	if err := a.pool.QueryRow(r.Context(),
		`INSERT INTO api_tokens (user_id, token, name) VALUES ($1,$2,$3) RETURNING id`,
		uid, token, name).Scan(&id); err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not create token"})
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "name": name, "token": token})
}

// POST /account/tokens/revoke {id}
func (a *Auth) HandleRevokeToken(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == 0 {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	ct, err := a.pool.Exec(r.Context(),
		`UPDATE api_tokens SET revoked=true WHERE id=$1 AND user_id=$2 AND revoked=false`, body.ID, uid)
	if err != nil || ct.RowsAffected() == 0 {
		writeJSON(w, 404, map[string]string{"error": "token not found"})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
