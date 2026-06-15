// internal/auth/connections.go
//
// Lists a logged-in user's linked OAuth identities for the Account header.
// Reuses /auth/oauth/{provider}/start for linking (already mounted) — when a
// logged-in user hits start, the callback's find-or-create will attach the
// identity to a NEW user unless we link to the current session. To keep this
// change small and safe, we expose the list here; linking-while-logged-in is a
// follow-up (the connectors feature). For now this powers the header display.
package auth

import (
	"net/http"
)

type connection struct {
	Provider string `json:"provider"`
	Email    string `json:"email"`
}

// GET /account/connections -> {username, connections:[{provider,email}]}
func (a *Auth) HandleConnections(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	var username string
	if err := a.pool.QueryRow(r.Context(),
		`SELECT username FROM users WHERE id=$1`, uid).Scan(&username); err != nil {
		writeJSON(w, 500, map[string]string{"error": "lookup failed"})
		return
	}
	rows, err := a.pool.Query(r.Context(),
		`SELECT provider, COALESCE(email,'') FROM oauth_identities WHERE user_id=$1 ORDER BY provider`, uid)
	conns := []connection{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var c connection
			if rows.Scan(&c.Provider, &c.Email) == nil {
				conns = append(conns, c)
			}
		}
	}
	writeJSON(w, 200, map[string]any{
		"username":    username,
		"connections": conns,
	})
}
