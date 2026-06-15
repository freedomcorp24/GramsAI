// internal/auth/landing_page.go
// Public marketing landing page for logged-out visitors. HTML lives in
// landing.html (embedded). Served at GET /welcome; nginx 302s unauthenticated
// users here. Logged-in visitors are bounced straight into the app.
package auth

import (
	_ "embed"
	"net/http"
)

//go:embed landing.html
var landingHTML []byte

// LandingPage serves the standalone landing UI.
func (a *Auth) LandingPage(w http.ResponseWriter, r *http.Request) {
	// Already authenticated? Don't show marketing — go to the app.
	if c, err := r.Cookie(CookieName); err == nil {
		if _, err := a.Resolve(r.Context(), c.Value); err == nil {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(landingHTML)
}
