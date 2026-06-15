// internal/auth/paid.go
// Payment gate for nginx auth_request. Distinct from HandleCheck (which only
// validates the session): HandlePaidCheck ALSO requires the account to be
// active and either unmetered or within a paid subscription window.
package auth

import "net/http"

// HandlePaidCheck: 200 if logged in AND active AND (unmetered OR paid_until>now),
// 403 if logged-in-but-unpaid (nginx redirects these to /subscribe), 401 if no
// valid session at all.
func (a *Auth) HandlePaidCheck(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		w.WriteHeader(401)
		return
	}
	uid, err := a.Resolve(r.Context(), c.Value)
	if err != nil {
		w.WriteHeader(401)
		return
	}
	var status string
	var unmetered bool
	var paidValid bool
	err = a.pool.QueryRow(r.Context(), `
		SELECT status, unmetered, (paid_until IS NOT NULL AND paid_until > now())
		FROM users WHERE id=$1`, uid).Scan(&status, &unmetered, &paidValid)
	if err != nil {
		w.WriteHeader(401)
		return
	}
	if status == "suspended" {
		w.WriteHeader(403) // auth_request only allows 401/403; 403 -> /subscribe
		return
	}
	if unmetered || paidValid {
		w.WriteHeader(200)
		return
	}
	w.WriteHeader(403) // logged in but no active subscription -> /subscribe
}
