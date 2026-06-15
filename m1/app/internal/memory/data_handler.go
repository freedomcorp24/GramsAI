// internal/memory/data_handler.go
//
// Step 2 of Privacy & Security: data export + delete-all-chats.
// Cookie-authed (getUID), same as crud_handler.go.
//
//   GET  /account/export            -> JSON download: memory (all types) + account + chats
//   POST /account/chats/delete-all  -> wipe chats (M2 container db) + episodes;
//                                      preserves facts + procedures
//
// Chats are pulled live from the user's running container (same path as the
// episode worker). Delete-all calls the control-agent /wipe-chats then removes
// episode rows (mem_type='episode') from user_memory.

package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// AgentWiper calls the control-agent /wipe-chats for a user's host.
// Supplied by main.go (it knows agent URLs + secret, like router.agentSpawn).
type AgentWiper func(ctx context.Context, userID int64) error

// DataHandler returns the handler for export + delete-all (cookie-authed).
func (s *Store) DataHandler(getUID GetUIDFunc, wipe AgentWiper) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := getUID(r)
		if uid == 0 {
			http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
			return
		}
		suffix := r.URL.Path
		switch {
		case suffix == "/account/export" && r.Method == http.MethodGet:
			s.handleExport(w, r, uid)
		case suffix == "/account/chats/delete-all" && r.Method == http.MethodPost:
			s.handleDeleteAllChats(w, r, uid, wipe)
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	}
}

// GET /account/export
func (s *Store) handleExport(w http.ResponseWriter, r *http.Request, uid int64) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	export := map[string]interface{}{
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"user_id":     uid,
	}

	// --- account info ---
	var acct struct {
		Username  string    `json:"username"`
		Email     string    `json:"email"`
		Tier      string    `json:"tier"`
		CreatedAt time.Time `json:"created_at"`
		PaidUntil *time.Time `json:"paid_until,omitempty"`
	}
	_ = s.pool.QueryRow(ctx, `
		SELECT COALESCE(username,''), COALESCE(email,''), COALESCE(tier,''), created_at, paid_until
		FROM users WHERE id=$1`, uid).Scan(&acct.Username, &acct.Email, &acct.Tier, &acct.CreatedAt, &acct.PaidUntil)
	export["account"] = acct

	// --- memory: all types, decrypted ---
	mem := map[string]interface{}{}
	for _, t := range []string{"fact", "procedure", "episode"} {
		ms, err := s.LoadMemories(ctx, uid, t)
		if err != nil {
			mem[t] = []interface{}{}
			continue
		}
		mem[t] = ms
	}
	export["memory"] = mem

	// --- chats: pull live from the user's running container ---
	chats := []interface{}{}
	var ip string
	var port int
	row := s.pool.QueryRow(ctx, `
		SELECT h.internal_ip, c.port
		FROM containers c JOIN hosts h ON h.id = c.host_id
		WHERE c.user_id=$1 AND c.status='running'`, uid)
	if row.Scan(&ip, &port) == nil && ip != "" && port > 0 {
		base := fmt.Sprintf("http://%s:%d", ip, port)
		if sessions, err := ocGetSessions(ctx, base); err == nil {
			for _, sess := range sessions {
				msgs, merr := ocGetMessages(ctx, base, sess.ID)
				if merr != nil {
					continue
				}
				msgOut := make([]map[string]string, 0, len(msgs))
				for _, m := range msgs {
					text := m.Info.Content
					if text == "" {
						for _, p := range m.Parts {
							if p.Type == "text" && p.Text != "" {
								text += p.Text
							}
						}
					}
					msgOut = append(msgOut, map[string]string{"role": m.Info.Role, "content": text})
				}
				chats = append(chats, map[string]interface{}{
					"session_id": sess.ID,
					"slug":       sess.Slug,
					"messages":   msgOut,
				})
			}
		}
	}
	export["chats"] = chats
	export["chats_note"] = "Chats are exported from the active session container. If no container is running, this list may be empty — open the app first to include chat history."

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="gramsai-export-%d.json"`, time.Now().Unix()))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(export)
}

// POST /account/chats/delete-all
func (s *Store) handleDeleteAllChats(w http.ResponseWriter, r *http.Request, uid int64, wipe AgentWiper) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	w.Header().Set("Content-Type", "application/json")

	// 1. wipe the container's chat db (control-agent /wipe-chats)
	if wipe != nil {
		if err := wipe(ctx, uid); err != nil {
			// non-fatal: still clear episodes below, but report
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok": false, "error": "chat wipe failed: " + err.Error(),
			})
			return
		}
	}

	// 2. delete episodes (derived from chats). Keep facts + procedures.
	_, _ = s.pool.Exec(ctx, `DELETE FROM user_memory WHERE user_id=$1 AND mem_type='episode'`, uid)
	// also clear per-session episode tracking so they re-summarize fresh
	_, _ = s.pool.Exec(ctx, `DELETE FROM session_episodes WHERE user_id=$1`, uid)
	// invalidate any episode cache
	s.invalidate(ctx, uid, "episode")

	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// helper to build the wipe POST body (used by main.go's AgentWiper closure)
func WipeChatsBody(userID int64) *bytes.Reader {
	b, _ := json.Marshal(map[string]any{"user_id": userID})
	return bytes.NewReader(b)
}
