// internal/memory/search_handler.go
//
// HTTP endpoint POST /api/memory/search — the container's search_memory MCP tool
// calls this. Authenticates by Bearer api_token (same as the LLM proxy), embeds
// the query, vector-searches the user's episodes, decrypts, returns JSON.
//
// Request:  {"query":"what did we discuss about X","k":5}
// Response: {"results":[{"content":"...","created_at":"..."}], "count":N}
package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tokenResolver resolves a Bearer token to a user id. Provided by main.go so we
// reuse the proxy's existing lookup (api_token / api_tokens table).
type TokenResolver func(ctx context.Context, pool *pgxpool.Pool, token string) (int64, error)

// SearchHandler returns the HTTP handler for POST /api/memory/search.
func (s *Store) SearchHandler(pool *pgxpool.Pool, orKey string, resolve TokenResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			http.Error(w, `{"error":"no token"}`, http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		uid, err := resolve(ctx, pool, token)
		if err != nil || uid == 0 {
			http.Error(w, `{"error":"bad token"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			Query string `json:"query"`
			K     int    `json:"k"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Query) == "" {
			http.Error(w, `{"error":"missing query"}`, http.StatusBadRequest)
			return
		}
		if req.K <= 0 || req.K > 20 {
			req.K = 5
		}

		// fail-open: no DEK / no episodes -> empty result, not an error.
		eps, serr := s.SearchEpisodes(ctx, uid, req.Query, orKey, req.K)
		if serr != nil {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": []interface{}{}, "count": 0})
			return
		}
		type result struct {
			Content   string    `json:"content"`
			CreatedAt time.Time `json:"created_at"`
		}
		out := make([]result, 0, len(eps))
		for _, e := range eps {
			out = append(out, result{Content: e.Content, CreatedAt: e.CreatedAt})
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": out, "count": len(out)})
	}
}
