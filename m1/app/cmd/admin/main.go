// cmd/admin/main.go
// GramsAI Admin API — SEPARATE binary from the gateway.
// Binds 127.0.0.1 ONLY. Reachable solely via the M1 admin nginx vhost,
// which itself is fronted by a Tor onion (client-auth) on the Whonix Gateway.
// Never exposed to containers or clearnet.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	pool        *pgxpool.Pool
	adminUser   string
	adminPass   string // compared in constant time. Set via env.
	agentSecret string // to command container stop on suspend/delete
	sessions    = struct {
		sync.RWMutex
		m map[string]time.Time
	}{m: map[string]time.Time{}}
)

const sessionTTL = 2 * time.Hour

func main() {
	listen := envOr("ADMIN_LISTEN", "127.0.0.1:8081")
	dbURL := envOr("DATABASE_URL", "")
	adminUser = envOr("ADMIN_USER", "admin")
	adminPass = envOr("ADMIN_PASS", "")
	agentSecret = envOr("AGENT_SECRET", "")
	if dbURL == "" || adminPass == "" {
		log.Fatal("DATABASE_URL and ADMIN_PASS are required")
	}

	ctx := context.Background()
	var err error
	pool, err = pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", handleLogin)
	mux.HandleFunc("/api/logout", handleLogout)
	mux.HandleFunc("/api/me", auth(handleMe))
	mux.HandleFunc("/api/users", auth(handleUsers))           // GET list
	mux.HandleFunc("/api/user/budget", auth(handleSetBudget)) // POST {id, budget_micros, daily_limit_micros}
	mux.HandleFunc("/api/user/token", auth(handleMintToken))  // POST {id}
	mux.HandleFunc("/api/user/suspend", auth(handleSuspend))  // POST {id, suspend bool}
	mux.HandleFunc("/api/user/delete", auth(handleDelete))    // POST {id}
	mux.HandleFunc("/api/user/tier", auth(handleSetTier))     // POST {id, tier}
	mux.HandleFunc("/api/user/unmetered", auth(handleUnmetered)) // POST {id, unmetered bool}
	mux.HandleFunc("/api/user/addtime", auth(handleAddTime))  // POST {id, days}
	mux.HandleFunc("/api/usage", auth(handleUsage))
	mux.HandleFunc("/api/payments", auth(handlePayments))           // GET recent usage_logs
	mux.HandleFunc("/api/tiers", auth(handleTiers))           // GET/POST tier defaults
	mux.HandleFunc("/api/users/stale", auth(handleStaleUsers)) // GET accounts lapsed > N days (default 90)

	// MUST bind localhost only.
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("admin-api listening on %s (localhost-only)", listen)
	srv := &http.Server{Handler: securityHeaders(mux), ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second}
	log.Fatal(srv.Serve(ln))
}

// ---- auth ----

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	var body struct{ User, Pass string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	uOK := subtle.ConstantTimeCompare([]byte(body.User), []byte(adminUser)) == 1
	pOK := subtle.ConstantTimeCompare([]byte(body.Pass), []byte(adminPass)) == 1
	if !uOK || !pOK {
		time.Sleep(time.Second) // throttle brute force
		http.Error(w, `{"error":"invalid credentials"}`, 401)
		return
	}
	tok := randHex(32)
	sessions.Lock()
	sessions.m[tok] = time.Now().Add(sessionTTL)
	sessions.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: "admin_session", Value: tok, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds()),
	})
	writeJSON(w, map[string]any{"ok": true})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("admin_session"); err == nil {
		sessions.Lock()
		delete(sessions.m, c.Value)
		sessions.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "admin_session", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, map[string]any{"ok": true})
}

func auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("admin_session")
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		sessions.RLock()
		exp, ok := sessions.m[c.Value]
		sessions.RUnlock()
		if !ok || time.Now().After(exp) {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		next(w, r)
	}
}

func handleMe(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{"user": adminUser}) }

// ---- users ----

func handleUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := pool.Query(r.Context(), `
		SELECT id, username, COALESCE(email,''), tier,
		       compute_budget_micros, compute_used_micros,
		       daily_used_micros, daily_limit_micros,
		       (api_token IS NOT NULL) AS has_token,
		       status, unmetered, paid_until
		FROM users ORDER BY id`)
	if err != nil {
		httpErr(w, err)
		return
	}
	defer rows.Close()
	type U struct {
		ID                                                          int64
		Username, Email, Tier                                       string
		BudgetMicros, UsedMicros, DailyUsedMicros, DailyLimitMicros int64
		HasToken                                                    bool
		Status                                                      string
		Unmetered                                                   bool
		PaidUntil                                                   *time.Time
	}
	out := []U{}
	for rows.Next() {
		var u U
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Tier, &u.BudgetMicros, &u.UsedMicros, &u.DailyUsedMicros, &u.DailyLimitMicros, &u.HasToken, &u.Status, &u.Unmetered, &u.PaidUntil); err != nil {
			httpErr(w, err)
			return
		}
		out = append(out, u)
	}
	writeJSON(w, out)
}

func handleSetBudget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	var b struct {
		ID               int64 `json:"id"`
		BudgetMicros     int64 `json:"budget_micros"`
		DailyLimitMicros int64 `json:"daily_limit_micros"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	_, err := pool.Exec(r.Context(), `
		UPDATE users SET compute_budget_micros=$2, daily_limit_micros=$3,
		                 compute_budget_cents=$2/10000, daily_limit_cents=$3/10000,
		                 updated_at=now() WHERE id=$1`, b.ID, b.BudgetMicros, b.DailyLimitMicros)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func handleMintToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	var b struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	tok := "gsk-" + randHex(32)
	ct, err := pool.Exec(r.Context(), `UPDATE users SET api_token=$2 WHERE id=$1`, b.ID, tok)
	if err != nil {
		httpErr(w, err)
		return
	}
	if ct.RowsAffected() == 0 {
		http.Error(w, `{"error":"no such user"}`, 404)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "token": tok})
}

// handleSuspend sets status active/suspended. On suspend, immediately stops the
// user's running container so they're booted now (not after idle).
func handleSuspend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	var b struct {
		ID      int64 `json:"id"`
		Suspend bool  `json:"suspend"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	status := "active"
	if b.Suspend {
		status = "suspended"
	}
	_, err := pool.Exec(r.Context(), `UPDATE users SET status=$2, updated_at=now() WHERE id=$1`, b.ID, status)
	if err != nil {
		httpErr(w, err)
		return
	}
	if b.Suspend {
		stopUserContainer(r.Context(), b.ID) // boot them now
	}
	writeJSON(w, map[string]any{"ok": true, "status": status})
}

// handleDelete stops the container, then deletes the user (cascades to
// containers/sessions/usage_logs via FK ON DELETE CASCADE).
func handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	var b struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	// Stop the container first so we don't orphan it on the worker host.
	stopUserContainer(r.Context(), b.ID)
	ct, err := pool.Exec(r.Context(), `DELETE FROM users WHERE id=$1`, b.ID)
	if err != nil {
		httpErr(w, err)
		return
	}
	if ct.RowsAffected() == 0 {
		http.Error(w, `{"error":"no such user"}`, 404)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleSetTier sets the tier and applies that tier's budget + daily limit from
// tier_defaults (if present). Tier names: basic, pro, max, ultra.
func handleSetTier(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	var b struct {
		ID   int64  `json:"id"`
		Tier string `json:"tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	ctx := r.Context()
	// Look up tier defaults; if found, apply budget+daily too.
	var budget, daily int64
	err := pool.QueryRow(ctx, `SELECT budget_micros, daily_limit_micros FROM tier_defaults WHERE tier=$1`, b.Tier).Scan(&budget, &daily)
	if err == nil {
		_, err = pool.Exec(ctx, `
			UPDATE users SET tier=$2, compute_budget_micros=$3::bigint, daily_limit_micros=$4::bigint,
			                 compute_budget_cents=$3::bigint/10000, daily_limit_cents=$4::bigint/10000,
			                 updated_at=now() WHERE id=$1`, b.ID, b.Tier, budget, daily)
	} else {
		// No defaults row — just set the tier name.
		_, err = pool.Exec(ctx, `UPDATE users SET tier=$2, updated_at=now() WHERE id=$1`, b.ID, b.Tier)
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "tier": b.Tier})
}

// handleUnmetered toggles the unmetered (unlimited) flag.
func handleUnmetered(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	var b struct {
		ID        int64 `json:"id"`
		Unmetered bool  `json:"unmetered"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	_, err := pool.Exec(r.Context(), `UPDATE users SET unmetered=$2, updated_at=now() WHERE id=$1`, b.ID, b.Unmetered)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "unmetered": b.Unmetered})
}

// handleAddTime extends paid_until by N days. If expired/null, starts from now;
// if still valid, extends from the current expiry. days may be negative to revoke.
func handleAddTime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	var b struct {
		ID   int64 `json:"id"`
		Days int   `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	// GREATEST(now, paid_until) so adding time to an expired sub starts from now.
	var newUntil time.Time
	err := pool.QueryRow(r.Context(), `
		UPDATE users
		SET paid_until = GREATEST(now(), COALESCE(paid_until, now())) + ($2 || ' days')::interval,
		    updated_at = now()
		WHERE id=$1
		RETURNING paid_until`, b.ID, strconv.Itoa(b.Days)).Scan(&newUntil)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "paid_until": newUntil})
}

func handlePayments(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 2000 {
			limit = n
		}
	}
	rows, err := pool.Query(r.Context(), `
		SELECT p.id, p.user_id, COALESCE(u.username,'?'),
		       p.kind, COALESCE(p.tier,''), COALESCE(p.period,''),
		       p.price_usd_cents, COALESCE(p.pay_currency,''), p.status,
		       p.created_at, p.updated_at
		FROM payments p
		LEFT JOIN users u ON u.id = p.user_id
		ORDER BY p.id DESC LIMIT $1`, limit)
	if err != nil {
		httpErr(w, err)
		return
	}
	defer rows.Close()
	type P struct {
		ID         int64
		UserID     int64
		Username   string
		Kind       string
		Tier       string
		Period     string
		PriceCents int64
		PayCoin    string
		Status     string
		CreatedAt  time.Time
		UpdatedAt  time.Time
	}
	out := []P{}
	for rows.Next() {
		var p P
		if err := rows.Scan(&p.ID, &p.UserID, &p.Username, &p.Kind, &p.Tier, &p.Period,
			&p.PriceCents, &p.PayCoin, &p.Status, &p.CreatedAt, &p.UpdatedAt); err != nil {
			httpErr(w, err)
			return
		}
		out = append(out, p)
	}
	var revenueCents int64
	_ = pool.QueryRow(r.Context(), `
		SELECT COALESCE(SUM(price_usd_cents),0) FROM payments
		WHERE status IN ('finished','applied','confirmed')`).Scan(&revenueCents)
	writeJSON(w, map[string]any{"payments": out, "revenue_cents": revenueCents})
}

func handleUsage(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	rows, err := pool.Query(r.Context(), `
		SELECT user_id, specialty, model, input_tokens, output_tokens, cost_micros, created_at
		FROM usage_logs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		httpErr(w, err)
		return
	}
	defer rows.Close()
	type L struct {
		UserID           int64
		Specialty, Model string
		InTok, OutTok    int
		CostMicros       int64
		CreatedAt        time.Time
	}
	out := []L{}
	for rows.Next() {
		var l L
		if err := rows.Scan(&l.UserID, &l.Specialty, &l.Model, &l.InTok, &l.OutTok, &l.CostMicros, &l.CreatedAt); err != nil {
			httpErr(w, err)
			return
		}
		out = append(out, l)
	}
	writeJSON(w, out)
}

// handleTiers: GET returns tier defaults; POST updates them.
func handleTiers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, _ = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS tier_defaults (
		tier TEXT PRIMARY KEY, budget_micros BIGINT NOT NULL, daily_limit_micros BIGINT NOT NULL DEFAULT 0)`)
	if r.Method == http.MethodPost {
		var b struct {
			Tier             string `json:"tier"`
			BudgetMicros     int64  `json:"budget_micros"`
			DailyLimitMicros int64  `json:"daily_limit_micros"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, "bad", 400)
			return
		}
		_, err := pool.Exec(ctx, `
			INSERT INTO tier_defaults (tier, budget_micros, daily_limit_micros)
			VALUES ($1,$2,$3)
			ON CONFLICT (tier) DO UPDATE SET budget_micros=$2, daily_limit_micros=$3`,
			b.Tier, b.BudgetMicros, b.DailyLimitMicros)
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	rows, err := pool.Query(ctx, `SELECT tier, budget_micros, daily_limit_micros FROM tier_defaults ORDER BY budget_micros`)
	if err != nil {
		httpErr(w, err)
		return
	}
	defer rows.Close()
	type T struct {
		Tier                           string
		BudgetMicros, DailyLimitMicros int64
	}
	out := []T{}
	for rows.Next() {
		var t T
		rows.Scan(&t.Tier, &t.BudgetMicros, &t.DailyLimitMicros)
		out = append(out, t)
	}
	writeJSON(w, out)
}

// handleStaleUsers lists accounts whose subscription lapsed more than ?days (default
// 90) ago, for MANUAL review/deletion in the admin "Stale Accounts" tab. Read-only:
// it flags, it never deletes. Deletion stays the explicit /api/user/delete action.
func handleStaleUsers(w http.ResponseWriter, r *http.Request) {
	days := 90
	if d := r.URL.Query().Get("days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			days = n
		}
	}
	rows, err := pool.Query(r.Context(), `
		SELECT u.id, u.username, COALESCE(u.email,''), u.tier, u.paid_until,
		       COALESCE(u.storage_used_bytes,0),
		       COALESCE(c.status,'none') AS container_status
		FROM users u
		LEFT JOIN containers c ON c.user_id = u.id
		WHERE u.paid_until IS NOT NULL
		  AND u.paid_until < now() - ($1 || ' days')::interval
		ORDER BY u.paid_until ASC`, strconv.Itoa(days))
	if err != nil {
		httpErr(w, err)
		return
	}
	defer rows.Close()
	type S struct {
		ID              int64
		Username, Email string
		Tier            string
		PaidUntil       *time.Time
		StorageUsed     int64
		ContainerStatus string
	}
	out := []S{}
	for rows.Next() {
		var s S
		if err := rows.Scan(&s.ID, &s.Username, &s.Email, &s.Tier, &s.PaidUntil, &s.StorageUsed, &s.ContainerStatus); err != nil {
			httpErr(w, err)
			return
		}
		out = append(out, s)
	}
	writeJSON(w, map[string]any{"days": days, "accounts": out})
}

// ---- container control (talks to the worker host's control-agent) ----

// stopUserContainer best-effort stops the user's container on its host and
// marks the DB row stopped. Used by suspend + delete. Silent on failure
// (the reaper / self-heal will reconcile).
func stopUserContainer(ctx context.Context, userID int64) {
	var controlURL string
	err := pool.QueryRow(ctx, `
		SELECT h.control_url FROM containers c JOIN hosts h ON h.id=c.host_id
		WHERE c.user_id=$1`, userID).Scan(&controlURL)
	if err != nil {
		return // no container row; nothing to stop
	}
	if agentSecret != "" && controlURL != "" {
		body, _ := json.Marshal(map[string]any{"user_id": userID})
		req, _ := http.NewRequestWithContext(ctx, "POST", controlURL+"/stop", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Agent-Secret", agentSecret)
		client := &http.Client{Timeout: 10 * time.Second}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	_, _ = pool.Exec(ctx, `UPDATE containers SET status='stopped' WHERE user_id=$1`, userID)
}

// ---- helpers ----

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, err error) {
	log.Printf("admin error: %v", err)
	http.Error(w, `{"error":"server error"}`, 500)
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
