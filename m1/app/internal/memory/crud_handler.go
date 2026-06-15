// internal/memory/crud_handler.go
//
// HTTP endpoints for the Settings > Security & Privacy memory UI.
// COOKIE/session authed (same as /account/* endpoints) via a getUID closure
// supplied by main.go. NOT Bearer — the web UI calls these with credentials:include.
//
//   GET    /account/memory          -> list facts + procedures (decrypted; episodes excluded)
//   POST   /account/memory          -> add a fact|procedure   {type,category,content}
//   PATCH  /account/memory/{id}     -> edit (supersede)        {type,category,content}
//   DELETE /account/memory/{id}     -> delete                  ?type=fact|procedure
//   POST   /account/memory/toggle   -> set memory_enabled      {enabled:true|false}
//   GET    /account/memory/status   -> {enabled:bool, counts:{fact,procedure,episode}}
//
// Episodes are intentionally NOT listed/editable — auto-generated summaries used
// by search_memory; users only manage facts/procedures.

package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const userMemoryCap = 200 // safety cap on user-created memories per type

// GetUIDFunc resolves a request's session cookie to a user id (0 = unauth).
// Supplied by main.go (same closure used for /account/* and the router).
type GetUIDFunc func(r *http.Request) int64

func validUserType(t string) bool { return t == "fact" || t == "procedure" }

// CRUDHandler returns the handler for /account/memory* (cookie-authed).
func (s *Store) CRUDHandler(getUID GetUIDFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		uid := getUID(r)
		if uid == 0 {
			http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		// suffix after /account/memory : "" | "toggle" | "status" | "{id}"
		suffix := strings.TrimPrefix(r.URL.Path, "/account/memory")
		suffix = strings.Trim(suffix, "/")

		switch {
		case suffix == "status" && r.Method == http.MethodGet:
			s.handleStatus(ctx, w, uid)
		case suffix == "toggle" && r.Method == http.MethodPost:
			s.handleToggle(ctx, w, r, uid)
		case suffix == "" && r.Method == http.MethodGet:
			s.handleList(ctx, w, uid)
		case suffix == "" && r.Method == http.MethodPost:
			s.handleCreate(ctx, w, r, uid)
		case suffix != "" && r.Method == http.MethodPatch:
			s.handleEdit(ctx, w, r, uid, suffix)
		case suffix != "" && r.Method == http.MethodDelete:
			s.handleDelete(ctx, w, r, uid, suffix)
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	}
}

// GET -> facts + procedures merged (episodes excluded), newest first.
func (s *Store) handleList(ctx context.Context, w http.ResponseWriter, uid int64) {
	out := make([]Memory, 0, 32)
	for _, t := range []string{"fact", "procedure"} {
		ms, err := s.LoadMemories(ctx, uid, t)
		if err != nil {
			continue // fail-open
		}
		out = append(out, ms...)
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": out, "count": len(out)})
}

// POST  {type,category,content}
func (s *Store) handleCreate(ctx context.Context, w http.ResponseWriter, r *http.Request, uid int64) {
	var req struct {
		Type     string `json:"type"`
		Category string `json:"category"`
		Content  string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
		return
	}
	req.Type = strings.TrimSpace(req.Type)
	req.Content = strings.TrimSpace(req.Content)
	if !validUserType(req.Type) {
		http.Error(w, `{"error":"type must be fact or procedure"}`, http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, `{"error":"content required"}`, http.StatusBadRequest)
		return
	}
	if req.Category == "" {
		req.Category = "general"
	}
	if n, _ := s.CountLive(ctx, uid, req.Type); n >= userMemoryCap {
		http.Error(w, `{"error":"memory limit reached"}`, http.StatusConflict)
		return
	}
	if err := s.StoreMemory(ctx, uid, req.Type, req.Category, req.Content, 1.0, "user"); err != nil {
		http.Error(w, `{"error":"store failed"}`, http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// PATCH /{id}  {type,category,content}
func (s *Store) handleEdit(ctx context.Context, w http.ResponseWriter, r *http.Request, uid int64, idStr string) {
	id, perr := strconv.ParseInt(idStr, 10, 64)
	if perr != nil {
		http.Error(w, `{"error":"bad id"}`, http.StatusBadRequest)
		return
	}
	var req struct {
		Type     string `json:"type"`
		Category string `json:"category"`
		Content  string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
		return
	}
	req.Type = strings.TrimSpace(req.Type)
	req.Content = strings.TrimSpace(req.Content)
	if !validUserType(req.Type) {
		http.Error(w, `{"error":"type must be fact or procedure"}`, http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, `{"error":"content required"}`, http.StatusBadRequest)
		return
	}
	if req.Category == "" {
		req.Category = "general"
	}
	if err := s.SupersedeMemory(ctx, uid, id, req.Type, req.Category, req.Content, 1.0, "user"); err != nil {
		http.Error(w, `{"error":"edit failed"}`, http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// DELETE /{id}?type=fact|procedure
func (s *Store) handleDelete(ctx context.Context, w http.ResponseWriter, r *http.Request, uid int64, idStr string) {
	id, perr := strconv.ParseInt(idStr, 10, 64)
	if perr != nil {
		http.Error(w, `{"error":"bad id"}`, http.StatusBadRequest)
		return
	}
	t := r.URL.Query().Get("type")
	if !validUserType(t) {
		t = "fact"
	}
	if err := s.DeleteMemory(ctx, uid, id, t); err != nil {
		http.Error(w, `{"error":"delete failed"}`, http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// POST /toggle  {enabled:bool}
func (s *Store) handleToggle(ctx context.Context, w http.ResponseWriter, r *http.Request, uid int64) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
		return
	}
	if _, err := s.pool.Exec(ctx, `UPDATE users SET memory_enabled=$2 WHERE id=$1`, uid, req.Enabled); err != nil {
		http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "enabled": req.Enabled})
}

// GET /status -> {enabled, counts}
func (s *Store) handleStatus(ctx context.Context, w http.ResponseWriter, uid int64) {
	var enabled bool
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(memory_enabled,true) FROM users WHERE id=$1`, uid).Scan(&enabled)
	factN, _ := s.CountLive(ctx, uid, "fact")
	procN, _ := s.CountLive(ctx, uid, "procedure")
	epN, _ := s.CountLive(ctx, uid, "episode")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled": enabled,
		"counts":  map[string]int{"fact": factN, "procedure": procN, "episode": epN},
	})
}
