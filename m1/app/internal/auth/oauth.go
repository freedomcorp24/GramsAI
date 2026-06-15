// internal/auth/oauth.go
//
// Social login for GitHub + Google. Dependency-free OAuth2 (standard net/http).
//
// Flow:
//   GET  /auth/oauth/{provider}/start     -> 302 to provider consent
//   GET  /auth/oauth/{provider}/callback  -> exchange code, fetch profile,
//                                            find-or-create-or-link user, set cookie, 302 to "/"
//
// Credentials come from env (loaded via systemd EnvironmentFile):
//   GITHUB_CLIENT_ID / GITHUB_CLIENT_SECRET
//   GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET
//   PUBLIC_BASE_URL (defaults https://grams.chat)
//
// Email from the provider is stored for display only; never used to send mail.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// httpClient is used for OAuth token exchange + profile fetches. Generous
// timeout because Whonix/Tor egress is slow; still bounded so a hung provider
// can't wedge the handler.
var httpClient = &http.Client{Timeout: 30 * time.Second}

type oauthProvider struct {
	name        string
	clientID    string
	clientSec   string
	authURL     string
	tokenURL    string
	scope       string
	userURL     string
	// extractor pulls (provider_uid, email, suggestedUsername) from the
	// provider's userinfo JSON.
	extract func(raw []byte, bearer string, fetch func(u string) ([]byte, error)) (uid, email, uname string, err error)
}

func (a *Auth) oauthProviders() map[string]*oauthProvider {
	base := envOrLocal("PUBLIC_BASE_URL", "https://grams.chat")
	_ = base
	return map[string]*oauthProvider{
		"github": {
			name:      "github",
			clientID:  os.Getenv("GITHUB_CLIENT_ID"),
			clientSec: os.Getenv("GITHUB_CLIENT_SECRET"),
			authURL:   "https://github.com/login/oauth/authorize",
			tokenURL:  "https://github.com/login/oauth/access_token",
			scope:     "read:user user:email",
			userURL:   "https://api.github.com/user",
			extract: func(raw []byte, bearer string, fetch func(string) ([]byte, error)) (string, string, string, error) {
				var u struct {
					ID    int64  `json:"id"`
					Login string `json:"login"`
					Email string `json:"email"`
				}
				if err := json.Unmarshal(raw, &u); err != nil {
					return "", "", "", err
				}
				email := u.Email
				if email == "" {
					// GitHub hides email by default; hit the emails endpoint.
					if eb, err := fetch("https://api.github.com/user/emails"); err == nil {
						var es []struct {
							Email    string `json:"email"`
							Primary  bool   `json:"primary"`
							Verified bool   `json:"verified"`
						}
						if json.Unmarshal(eb, &es) == nil {
							for _, e := range es {
								if e.Primary && e.Verified {
									email = e.Email
									break
								}
							}
						}
					}
				}
				return fmt.Sprintf("%d", u.ID), email, u.Login, nil
			},
		},
		"google": {
			name:      "google",
			clientID:  os.Getenv("GOOGLE_CLIENT_ID"),
			clientSec: os.Getenv("GOOGLE_CLIENT_SECRET"),
			authURL:   "https://accounts.google.com/o/oauth2/v2/auth",
			tokenURL:  "https://oauth2.googleapis.com/token",
			scope:     "openid email profile",
			userURL:   "https://openidconnect.googleapis.com/v1/userinfo",
			extract: func(raw []byte, bearer string, fetch func(string) ([]byte, error)) (string, string, string, error) {
				var u struct {
					Sub   string `json:"sub"`
					Email string `json:"email"`
					Name  string `json:"name"`
				}
				if err := json.Unmarshal(raw, &u); err != nil {
					return "", "", "", err
				}
				uname := u.Name
				if i := strings.IndexByte(u.Email, '@'); i > 0 && uname == "" {
					uname = u.Email[:i]
				}
				return u.Sub, u.Email, uname, nil
			},
		},
	}
}

func envOrLocal(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func (a *Auth) redirectURI(provider string) string {
	base := envOrLocal("PUBLIC_BASE_URL", "https://grams.chat")
	return strings.TrimRight(base, "/") + "/auth/oauth/" + provider + "/callback"
}

// GET /auth/oauth/{provider}/start
func (a *Auth) HandleOAuthStart(w http.ResponseWriter, r *http.Request) {
	pname := chi.URLParam(r, "provider")
	p := a.oauthProviders()[pname]
	if p == nil || p.clientID == "" {
		http.Redirect(w, r, "/login?oauth=unavailable", http.StatusFound)
		return
	}
	state := randHex(24)
	_, _ = a.pool.Exec(r.Context(),
		`INSERT INTO oauth_state (state, provider, expires_at) VALUES ($1,$2,$3)`,
		state, pname, time.Now().Add(10*time.Minute))

	q := url.Values{}
	q.Set("client_id", p.clientID)
	q.Set("redirect_uri", a.redirectURI(pname))
	q.Set("scope", p.scope)
	q.Set("state", state)
	q.Set("response_type", "code")
	if pname == "google" {
		q.Set("access_type", "online")
		q.Set("prompt", "select_account")
	}
	http.Redirect(w, r, p.authURL+"?"+q.Encode(), http.StatusFound)
}

// GET /auth/oauth/{provider}/callback
func (a *Auth) HandleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	pname := chi.URLParam(r, "provider")
	p := a.oauthProviders()[pname]
	if p == nil || p.clientID == "" {
		http.Redirect(w, r, "/login?oauth=unavailable", http.StatusFound)
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Redirect(w, r, "/login?oauth=denied", http.StatusFound)
		return
	}
	// validate + consume state
	var sprov string
	err := a.pool.QueryRow(r.Context(),
		`DELETE FROM oauth_state WHERE state=$1 AND expires_at>now() RETURNING provider`, state).
		Scan(&sprov)
	if err != nil || sprov != pname {
		http.Redirect(w, r, "/login?oauth=badstate", http.StatusFound)
		return
	}

	tok, err := a.oauthExchange(r.Context(), p, code)
	if err != nil || tok == "" {
		http.Redirect(w, r, "/login?oauth=exchange", http.StatusFound)
		return
	}

	fetch := func(u string) ([]byte, error) { return a.oauthGet(r.Context(), u, tok) }
	raw, err := fetch(p.userURL)
	if err != nil {
		http.Redirect(w, r, "/login?oauth=profile", http.StatusFound)
		return
	}
	uid, email, uname, err := p.extract(raw, tok, fetch)
	if err != nil || uid == "" {
		http.Redirect(w, r, "/login?oauth=profile", http.StatusFound)
		return
	}

	userID, err := a.findOrCreateOAuthUser(r.Context(), pname, uid, email, uname)
	if err != nil {
		http.Redirect(w, r, "/login?oauth=link", http.StatusFound)
		return
	}

	sess, err := a.issueSession(r.Context(), userID, r.UserAgent(), clientIP(r))
	if err != nil {
		http.Redirect(w, r, "/login?oauth=session", http.StatusFound)
		return
	}
	SetCookie(w, sess)
	http.Redirect(w, r, "/", http.StatusFound)
}

// oauthExchange swaps the code for an access token.
func (a *Auth) oauthExchange(ctx context.Context, p *oauthProvider, code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", p.clientID)
	form.Set("client_secret", p.clientSec)
	form.Set("code", code)
	form.Set("redirect_uri", a.redirectURI(p.name))
	form.Set("grant_type", "authorization_code")

	req, _ := http.NewRequestWithContext(ctx, "POST", p.tokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

// oauthGet fetches a provider API URL with the bearer token.
func (a *Auth) oauthGet(ctx context.Context, u, token string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "grams")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// findOrCreateOAuthUser links an existing identity, or creates a new account.
func (a *Auth) findOrCreateOAuthUser(ctx context.Context, provider, puid, email, uname string) (int64, error) {
	// already linked?
	var uid int64
	err := a.pool.QueryRow(ctx,
		`SELECT user_id FROM oauth_identities WHERE provider=$1 AND provider_uid=$2`, provider, puid).
		Scan(&uid)
	if err == nil {
		return uid, nil
	}
	if err != pgx.ErrNoRows {
		return 0, err
	}

	// new identity -> create a fresh user. Generate a unique, valid username.
	username := sanitizeUsername(uname)
	if username == "" {
		username = provider + randHex(4)
	}
	username = a.uniqueUsername(ctx, username)

	// budget default for basic tier (mirror Register()).
	var budgetMicros, dailyMicros int64 = 10_000_000, 0
	_ = a.pool.QueryRow(ctx,
		`SELECT budget_micros, daily_limit_micros FROM tier_defaults WHERE tier='basic'`).
		Scan(&budgetMicros, &dailyMicros)
	budgetCents := budgetMicros / 10000
	dailyCents := dailyMicros / 10000
	apiToken := "gsk-" + randHex(32)

	// OAuth accounts have no usable password (random hash; reset path is N/A).
	randPwHash := "$2a$10$" + randHex(22) // not a valid bcrypt verify target -> login-by-password impossible

	var newID int64
	insertUser := func(em any) error {
		return a.pool.QueryRow(ctx, `
			INSERT INTO users (username, pass_hash, tier, status, unmetered,
			                   api_token, compute_budget_micros, daily_limit_micros,
			                   compute_budget_cents, daily_limit_cents, email)
			VALUES ($1,$2,'basic','active',false,$3,$4,$5,$6,$7,$8)
			RETURNING id`,
			username, randPwHash, apiToken, budgetMicros, dailyMicros, budgetCents, dailyCents, em).
			Scan(&newID)
	}
	err = insertUser(nullIfEmpty(email))
	if err != nil && strings.Contains(err.Error(), "email") {
		// email already taken by another account -> create without email on the
		// users row (still recorded on oauth_identities below).
		err = insertUser(nil)
	}
	if err != nil {
		return 0, err
	}
	_, err = a.pool.Exec(ctx,
		`INSERT INTO oauth_identities (provider, provider_uid, user_id, email) VALUES ($1,$2,$3,$4)`,
		provider, puid, newID, nullIfEmpty(email))
	if err != nil {
		return 0, err
	}
	return newID, nil
}

// uniqueUsername appends digits until the username is free.
func (a *Auth) uniqueUsername(ctx context.Context, base string) string {
	try := base
	for i := 0; i < 50; i++ {
		var n int
		_ = a.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE username=$1`, try).Scan(&n)
		if n == 0 {
			return try
		}
		try = base + randHex(2)
	}
	return base + randHex(6)
}

func sanitizeUsername(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 32 {
		out = out[:32]
	}
	for len(out) < 3 {
		out += "0"
	}
	return out
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
