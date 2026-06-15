// internal/auth/login_page.go
// Serves the GramsAI login/signup page. HTML lives in login.html (embedded).
package auth

import (
	_ "embed"
	"net/http"
)

//go:embed login.html
var loginHTML []byte

// LoginPage serves the standalone auth UI. Mounted at GET /login.
func (a *Auth) LoginPage(w http.ResponseWriter, r *http.Request) {
	// If already authenticated, bounce to the app.
	if c, err := r.Cookie(CookieName); err == nil {
		if _, err := a.Resolve(r.Context(), c.Value); err == nil {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(loginHTML)
}
