// internal/router/router.go
// Per-user container router. Resolves an authenticated user to their own
// OpenCode container, spawning it on demand via the host's control-agent,
// and reverse-proxies frontend traffic to it. Backed by the hosts/containers
// registry so it scales across many worker hosts.
package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"gramsai/internal/memory"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GRAMSAI_DEK_ROUTER
type Router struct {
	pool        *pgxpool.Pool
	agentSecret string
	keys        *memory.KeyStore // per-user DEK store (for container encryption)
	mu          sync.Mutex       // serializes spawn decisions (port assignment races)
}

func New(pool *pgxpool.Pool, agentSecret string, keys *memory.KeyStore) *Router {
	return &Router{pool: pool, agentSecret: agentSecret, keys: keys}
}

type Target struct {
	UserID      int64
	HostID      int64
	InternalIP  string
	Port        int
	BrowserPort int
	Name        string
}

func (t Target) URL() string { return fmt.Sprintf("http://%s:%d", t.InternalIP, t.Port) }

// EnsureContainer returns a running container target for the user, spawning
// (or respawning after reap) as needed. Safe under concurrency.
func (r *Router) EnsureContainer(ctx context.Context, userID int64) (*Target, error) {
	// Fast path: already running per DB + agent confirms.
	if t, ok := r.lookupRunning(ctx, userID); ok {
		go r.touch(userID) // bump last_active
		return t, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Re-check under lock (another request may have spawned it).
	if t, ok := r.lookupRunning(ctx, userID); ok {
		go r.touch(userID)
		return t, nil
	}

	// Need to (re)spawn. Find the user's token + an existing or new placement.
	token, err := r.userToken(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("user token: %w", err)
	}

	t, err := r.placement(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Call the host's control-agent to spawn.
	if err := r.agentSpawn(ctx, t.HostID, userID, token, t.Port, t.BrowserPort); err != nil {
		_ = r.setStatus(ctx, userID, "error")
		return nil, fmt.Errorf("spawn: %w", err)
	}
	_ = r.setStatus(ctx, userID, "running")

	// Give OpenCode a moment to bind its port before we proxy to it.
	if !r.waitReady(t, 8*time.Second) {
		log.Printf("router: container for user %d not ready in time (continuing)", userID)
	}
	return t, nil
}

// lookupRunning returns the target if DB says running AND the agent confirms.
func (r *Router) lookupRunning(ctx context.Context, userID int64) (*Target, bool) {
	t := &Target{UserID: userID}
	var status string
	err := r.pool.QueryRow(ctx, `
		SELECT c.host_id, h.internal_ip, c.port, COALESCE(c.browser_port, 0), c.container_name, c.status
		FROM containers c JOIN hosts h ON h.id = c.host_id
		WHERE c.user_id = $1`, userID).
		Scan(&t.HostID, &t.InternalIP, &t.Port, &t.BrowserPort, &t.Name, &status)
	if err != nil {
		return nil, false
	}
	if status != "running" {
		return nil, false
	}
	// Verify the container actually responds. A manually-killed or crashed
	// container can leave a stale 'running' row; if the target refuses the
	// connection, treat it as not-running so the spawn path self-heals.
	if !r.alive(t) {
		_ = r.setStatus(ctx, userID, "stopped")
		return nil, false
	}
	return t, true
}

// alive does a fast HTTP probe of the container's OpenCode port.
func (r *Router) alive(t *Target) bool {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(t.URL() + "/global/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// placement returns an existing container row (for respawn) or allocates a new
// host+port for a first spawn.
func (r *Router) placement(ctx context.Context, userID int64) (*Target, error) {
	// Existing row? reuse host+port+name.
	t := &Target{UserID: userID}
	err := r.pool.QueryRow(ctx, `
		SELECT c.host_id, h.internal_ip, c.port, COALESCE(c.browser_port, 0), c.container_name
		FROM containers c JOIN hosts h ON h.id = c.host_id
		WHERE c.user_id=$1`, userID).
		Scan(&t.HostID, &t.InternalIP, &t.Port, &t.BrowserPort, &t.Name)
	if err == nil {
		// Backfill a browser port for legacy rows created before the browser_port column.
		if t.BrowserPort == 0 {
			var bp int
			if e := r.pool.QueryRow(ctx, `
				SELECT COALESCE(MAX(browser_port), 16001) + 1 FROM containers WHERE host_id = $1`, t.HostID).Scan(&bp); e == nil {
				if _, e2 := r.pool.Exec(ctx, `UPDATE containers SET browser_port=$1 WHERE user_id=$2`, bp, userID); e2 == nil {
					t.BrowserPort = bp
				}
			}
		}
		_ = r.setStatus(ctx, userID, "starting")
		return t, nil
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}

	// New placement: pick an active host with free capacity.
	var hostID int64
	var internalIP string
	err = r.pool.QueryRow(ctx, `
		SELECT h.id, h.internal_ip
		FROM hosts h
		WHERE h.active = true
		  AND (SELECT count(*) FROM containers c WHERE c.host_id = h.id) < h.capacity
		ORDER BY (SELECT count(*) FROM containers c WHERE c.host_id = h.id) ASC
		LIMIT 1`).Scan(&hostID, &internalIP)
	if err != nil {
		return nil, fmt.Errorf("no host with capacity: %w", err)
	}

	// Assign next free port on that host (base 5002, scan upward).
	var port int
	err = r.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(port), 5001) + 1 FROM containers WHERE host_id = $1`, hostID).
		Scan(&port)
	if err != nil {
		return nil, err
	}
	// Assign next free browser/bridge port on that host (independent range, base 16002).
	var browserPort int
	err = r.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(browser_port), 16001) + 1 FROM containers WHERE host_id = $1`, hostID).
		Scan(&browserPort)
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("oc-user-%d", userID)
	_, err = r.pool.Exec(ctx, `
		INSERT INTO containers (user_id, host_id, container_name, port, browser_port, status)
		VALUES ($1,$2,$3,$4,$5,'starting')`, userID, hostID, name, port, browserPort)
	if err != nil {
		return nil, fmt.Errorf("insert container: %w", err)
	}
	return &Target{UserID: userID, HostID: hostID, InternalIP: internalIP, Port: port, BrowserPort: browserPort, Name: name}, nil
}

func (r *Router) userToken(ctx context.Context, userID int64) (string, error) {
	var tok *string
	if err := r.pool.QueryRow(ctx, `SELECT api_token FROM users WHERE id=$1`, userID).Scan(&tok); err != nil {
		return "", err
	}
	if tok == nil || *tok == "" {
		return "", fmt.Errorf("user %d has no api_token", userID)
	}
	return *tok, nil
}

// ---- control-agent calls ----

func (r *Router) agentControlURL(ctx context.Context, hostID int64) (string, error) {
	var u string
	err := r.pool.QueryRow(ctx, `SELECT control_url FROM hosts WHERE id=$1`, hostID).Scan(&u)
	return u, err
}

func (r *Router) agentSpawn(ctx context.Context, hostID, userID int64, token string, port, browserPort int) error {
	base, err := r.agentControlURL(ctx, hostID)
	if err != nil {
		return err
	}
	// GRAMSAI_DEK_ROUTER: include the user's DEK so the container can encrypt storage.
	// Best-effort: if no DEK (Redis down / not logged in), container runs plaintext (fail-open).
	dekB64 := ""
	if r.keys != nil {
		if dek, derr := r.keys.Get(ctx, userID); derr == nil && len(dek) > 0 {
			dekB64 = base64.StdEncoding.EncodeToString(dek)
		}
	}
	body, _ := json.Marshal(map[string]any{"user_id": userID, "token": token, "port": port, "browser_port": browserPort, "dek": dekB64})
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/spawn", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Secret", r.agentSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("agent spawn status %d", resp.StatusCode)
	}
	return nil
}

// ---- status / housekeeping ----

// WipeChats calls the control-agent /wipe-chats for the user's host: removes the
// opencode db files (chats) while preserving workspace/config. Used by delete-all.
func (r *Router) WipeChats(ctx context.Context, userID int64) error {
	t, err := r.placement(ctx, userID)
	if err != nil {
		return err
	}
	base, err := r.agentControlURL(ctx, t.HostID)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"user_id": userID})
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/wipe-chats", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Secret", r.agentSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("wipe-chats status %d", resp.StatusCode)
	}
	return nil
}

func (r *Router) setStatus(ctx context.Context, userID int64, status string) error {
	_, err := r.pool.Exec(ctx, `UPDATE containers SET status=$2, last_active=now() WHERE user_id=$1`, userID, status)
	return err
}

func (r *Router) touch(userID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = r.pool.Exec(ctx, `UPDATE containers SET last_active=now() WHERE user_id=$1`, userID)
}

// waitReady polls the container's port until OpenCode answers or timeout.
func (r *Router) waitReady(t *Target, max time.Duration) bool {
	deadline := time.Now().Add(max)
	client := &http.Client{Timeout: 1 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(t.URL() + "/global/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return true
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	return false
}

// ---- reaper ----

// StartReaper stops containers idle longer than maxIdle, on an interval.
func (r *Router) StartReaper(maxIdle, interval time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			r.reapOnce(maxIdle)
		}
	}()
	log.Printf("router: reaper started (idle>%s, every %s)", maxIdle, interval)
}

func (r *Router) reapOnce(maxIdle time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, host_id FROM containers
		WHERE status='running' AND last_active < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int(maxIdle.Seconds())))
	if err != nil {
		log.Printf("reaper query: %v", err)
		return
	}
	type victim struct{ uid, hid int64 }
	var vs []victim
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.uid, &v.hid); err == nil {
			vs = append(vs, v)
		}
	}
	rows.Close()
	for _, v := range vs {
		if err := r.agentStop(ctx, v.hid, v.uid); err != nil {
			log.Printf("reaper stop user %d: %v", v.uid, err)
			continue
		}
		_ = r.setStatus(ctx, v.uid, "stopped")
		log.Printf("reaper: stopped idle container user=%d", v.uid)
	}
}

func (r *Router) agentStop(ctx context.Context, hostID, userID int64) error {
	base, err := r.agentControlURL(ctx, hostID)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"user_id": userID})
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/stop", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Secret", r.agentSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ---- reverse proxy ----

// Proxy returns an http.Handler that routes the request to the user's container.
// userID is resolved upstream (auth middleware) and passed in via getUserID.
func (r *Router) Proxy(getUserID func(*http.Request) int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		uid := getUserID(req)
		if uid == 0 {
			http.Redirect(w, req, "/login", http.StatusFound)
			return
		}
		t, err := r.EnsureContainer(req.Context(), uid)
		if err != nil {
			log.Printf("router: ensure container user %d: %v", uid, err)
			http.Error(w, "container unavailable", http.StatusBadGateway)
			return
		}
		target, _ := url.Parse(t.URL())
		// GRAMSAI_CWD_BIND: for a session message run, force ?directory=<worktree>
		// so the model's cwd binds to THIS chat's worktree (OpenCode #6697 leaves
		// Instance.directory stuck on master otherwise). Resolved server-side from
		// the session id in the path; bash, read_image and generated-image writes
		// all follow cwd, so this one injection fixes per-chat file isolation end
		// to end. Done before proxying so the body stream is untouched.
		if sid := sessionIDFromMessagePath(req.URL.Path); sid != "" {
			if wt, derr := r.chatDirectory(req, uid, sid); derr == nil && wt != "" {
				q := req.URL.Query()
				q.Set("directory", wt)
				req.URL.RawQuery = q.Encode()
				// GRAMSAI_STAGING drain: move any staged uploads into THIS worktree's
				// uploads/ before the model runs, so cwd-relative "uploads/<name>"
				// resolves. Best-effort; a drain failure must not block the run.
				r.drainStaging(req, uid, wt)
			}
		}
		rp := &httputil.ReverseProxy{
			Director: func(rq *http.Request) {
				rq.URL.Scheme = target.Scheme
				rq.URL.Host = target.Host
				rq.Host = target.Host
			},
			FlushInterval: -1, // immediate flush for SSE
			ModifyResponse: func(resp *http.Response) error {
				if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
					resp.Header.Set("X-Accel-Buffering", "no")
					resp.Header.Set("Cache-Control", "no-cache")
				}
				return nil
			},
		}
		rp.ServeHTTP(w, req)
	})
}

// sessionIDFromMessagePath returns the session id if the path is a session
// message run (".../session/<id>/message" or ".../session/<id>/command"),
// else "". The path may be prefixed by a base64 :dir segment, so we scan for
// the "session/" marker rather than anchoring at the root.
func sessionIDFromMessagePath(p string) string {
	const marker = "/session/"
	i := strings.Index(p, marker)
	if i < 0 {
		return ""
	}
	rest := p[i+len(marker):]
	// rest is "<id>/message" | "<id>/command" | "<id>" | "<id>/..."
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return ""
	}
	id := rest[:slash]
	tail := rest[slash+1:]
	if id == "" {
		return ""
	}
	if tail == "message" || tail == "command" ||
		strings.HasPrefix(tail, "message") || strings.HasPrefix(tail, "command") ||
		strings.HasPrefix(tail, "prompt") {
		return id
	}
	return ""
}

// drainStaging asks the user's control-agent to move any files from the user's
// staging dir into the given worktree's uploads/ dir. Called right before a
// session message runs. Best-effort: errors are swallowed so a drain hiccup
// never blocks the model. The agent does the actual move (rename) on the host.
func (r *Router) drainStaging(req *http.Request, uid int64, worktree string) {
	var controlURL string
	if err := r.pool.QueryRow(req.Context(),
		`SELECT h.control_url FROM containers c JOIN hosts h ON h.id = c.host_id WHERE c.user_id=$1`,
		uid).Scan(&controlURL); err != nil {
		return
	}
	u := fmt.Sprintf("%s/drain?user_id=%d&dir=%s", controlURL, uid, url.QueryEscape(worktree))
	areq, err := http.NewRequestWithContext(req.Context(), "POST", u, nil)
	if err != nil {
		return
	}
	areq.Header.Set("X-Agent-Secret", r.agentSecret)
	resp, err := http.DefaultClient.Do(areq)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// ---- storage usage poller ----

// StartStoragePoller refreshes users.storage_used_bytes from each host's
// control-agent on an interval. The LLM gateway reads this cached value to
// enforce per-tier storage quotas (it never calls the agent on the hot path).
func (r *Router) StartStoragePoller(interval time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			r.pollStorageOnce()
		}
	}()
	log.Printf("router: storage poller started (every %s)", interval)
}

func (r *Router) pollStorageOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rows, err := r.pool.Query(ctx, `SELECT user_id, host_id FROM containers`)
	if err != nil {
		log.Printf("storage poller query: %v", err)
		return
	}
	type entry struct{ uid, hid int64 }
	var es []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.uid, &e.hid); err == nil {
			es = append(es, e)
		}
	}
	rows.Close()
	for _, e := range es {
		bytes, err := r.agentUsage(ctx, e.hid, e.uid)
		if err != nil {
			log.Printf("storage poller usage user %d: %v", e.uid, err)
			continue
		}
		_, _ = r.pool.Exec(ctx,
			`UPDATE users SET storage_used_bytes=$2, storage_checked_at=now() WHERE id=$1`,
			e.uid, bytes)
	}
}

func (r *Router) agentUsage(ctx context.Context, hostID, userID int64) (int64, error) {
	base, err := r.agentControlURL(ctx, hostID)
	if err != nil {
		return 0, err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/usage?user_id=%d", base, userID), nil)
	req.Header.Set("X-Agent-Secret", r.agentSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("agent usage status %d", resp.StatusCode)
	}
	var out struct {
		Bytes int64 `json:"bytes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Bytes, nil
}

// ---- lifecycle: 7-day data-grace purge + 90-day stale flag ----

// StartLifecycle runs the account lifecycle sweep on an interval (daily in prod).
//   - users whose subscription lapsed > graceDays ago AND still have a live
//     container row get their container + data dir purged (via agent /purge),
//     and the row is marked status='purged' so we don't re-purge nightly. A
//     renewing user's spawn path rebuilds the dirs via ensureDirs.
//   - the account record itself is NEVER deleted here; 90-day stale accounts are
//     surfaced to the admin "Stale Accounts" view for MANUAL deletion.
func (r *Router) StartLifecycle(graceDays int, interval time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			r.lifecycleOnce(graceDays)
		}
	}()
	log.Printf("router: lifecycle started (grace %dd, every %s)", graceDays, interval)
}

func (r *Router) lifecycleOnce(graceDays int) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Lapsed past grace, container not yet purged -> purge data.
	rows, err := r.pool.Query(ctx, `
		SELECT c.user_id, c.host_id
		FROM containers c
		JOIN users u ON u.id = c.user_id
		WHERE c.status <> 'purged'
		  AND u.paid_until IS NOT NULL
		  AND u.paid_until < now() - ($1 || ' days')::interval`,
		fmt.Sprintf("%d", graceDays))
	if err != nil {
		log.Printf("lifecycle query: %v", err)
		return
	}
	type victim struct{ uid, hid int64 }
	var vs []victim
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.uid, &v.hid); err == nil {
			vs = append(vs, v)
		}
	}
	rows.Close()

	for _, v := range vs {
		if v.uid <= 0 {
			continue // never purge a non-positive id
		}
		if err := r.agentPurge(ctx, v.hid, v.uid); err != nil {
			log.Printf("lifecycle purge user %d: %v", v.uid, err)
			continue
		}
		_, _ = r.pool.Exec(ctx, `UPDATE containers SET status='purged' WHERE user_id=$1`, v.uid)
		log.Printf("lifecycle: purged data for lapsed user=%d (grace %dd elapsed)", v.uid, graceDays)
	}
}

func (r *Router) agentPurge(ctx context.Context, hostID, userID int64) error {
	if userID <= 0 {
		return fmt.Errorf("refusing purge for non-positive user id")
	}
	base, err := r.agentControlURL(ctx, hostID)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"user_id": userID})
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/purge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Secret", r.agentSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("agent purge status %d", resp.StatusCode)
	}
	return nil
}
