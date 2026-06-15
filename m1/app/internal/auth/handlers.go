// internal/auth/handlers.go
// HTTP endpoints for signup/login/logout/me, mounted by main.go.
package auth

import (
	"encoding/json"
	"errors"
	"net/http"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func clientIP(r *http.Request) string {
	// chi RealIP middleware sets RemoteAddr; good enough for the session log.
	return r.RemoteAddr
}

// POST /auth/register {username, password}
func (a *Auth) HandleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct{ Username, Password string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	_, err := a.Register(r.Context(), body.Username, body.Password)
	if err != nil {
		code := 400
		if errors.Is(err, ErrTaken) {
			code = 409
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	// Auto-login after register.
	token, err := a.Login(r.Context(), body.Username, body.Password, r.UserAgent(), clientIP(r))
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "registered but login failed"})
		return
	}
	SetCookie(w, token)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// POST /auth/login {username, password}
func (a *Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct{ Username, Password string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	token, twoFA, uid, err := a.LoginStep1(r.Context(), body.Username, body.Password, r.UserAgent(), clientIP(r))
	if err != nil {
		code := 401
		if errors.Is(err, ErrSuspended) {
			code = 403
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	if twoFA {
		pending, perr := a.newPendingChallenge(r.Context(), uid, r.UserAgent(), clientIP(r))
		if perr != nil {
			writeJSON(w, 500, map[string]string{"error": "could not start challenge"})
			return
		}
		writeJSON(w, 200, map[string]any{"needs_totp": true, "pending": pending})
		return
	}
	SetCookie(w, token)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// POST /auth/logout
func (a *Auth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		a.Logout(r.Context(), c.Value)
	}
	ClearCookie(w)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// GET /auth/me  -> {user_id, username, tier, status, unmetered}
func (a *Auth) HandleMe(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		writeJSON(w, 401, map[string]string{"error": "not authenticated"})
		return
	}
	uid, err := a.Resolve(r.Context(), c.Value)
	if err != nil {
		writeJSON(w, 401, map[string]string{"error": "not authenticated"})
		return
	}
	var (
		username, tier, status string
		unmetered              bool
		budgetMicros, usedMicros int64
	)
	err = a.pool.QueryRow(r.Context(), `
		SELECT username, tier, status, unmetered, compute_budget_micros, compute_used_micros
		FROM users WHERE id=$1`, uid).
		Scan(&username, &tier, &status, &unmetered, &budgetMicros, &usedMicros)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "lookup failed"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"user_id": uid, "username": username, "tier": tier,
		"status": status, "unmetered": unmetered,
		"budget_micros": budgetMicros, "used_micros": usedMicros,
	})
}

// HandleCheck is for nginx auth_request: 200 if the session is valid, 401 if not.
// No body — auth_request ignores it.
func (a *Auth) HandleCheck(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		w.WriteHeader(401)
		return
	}
	if _, err := a.Resolve(r.Context(), c.Value); err != nil {
		w.WriteHeader(401)
		return
	}
	w.WriteHeader(200)
}
