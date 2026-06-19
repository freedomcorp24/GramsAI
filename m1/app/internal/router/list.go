package router

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// HandleList returns the file listing of the caller's chat worktree as JSON.
// It resolves the session to the user's worktree (same chat->worktree resolution
// as HandleDownload), then proxies the agent's /ls. Feeds the in-chat file strip
// and the file-tree tab so EVERY tool-created file (generated images, bash
// outputs, uploads) is listed regardless of git status.
func (r *Router) HandleList(getUID func(*http.Request) int64) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		uid := getUID(req)
		if uid <= 0 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var controlURL string
		err := r.pool.QueryRow(req.Context(),
			`SELECT h.control_url FROM containers c JOIN hosts h ON h.id = c.host_id WHERE c.user_id=$1`,
			uid).Scan(&controlURL)
		if err != nil {
			http.Error(w, `{"error":"no container for user"}`, http.StatusNotFound)
			return
		}
		dir := req.URL.Query().Get("dir")
		if chat := req.URL.Query().Get("chat"); chat != "" {
			wt, derr := r.chatDirectory(req, uid, chat)
			if derr != nil || wt == "" {
				http.Error(w, `{"error":"chat not found"}`, http.StatusNotFound)
				return
			}
			dir = wt
		}
		agentURL := fmt.Sprintf("%s/ls?user_id=%d&dir=%s", controlURL, uid, url.QueryEscape(dir))
		areq, err := http.NewRequestWithContext(req.Context(), "GET", agentURL, nil)
		if err != nil {
			http.Error(w, `{"error":"bad agent request"}`, http.StatusInternalServerError)
			return
		}
		areq.Header.Set("X-Agent-Secret", r.agentSecret)
		resp, err := http.DefaultClient.Do(areq)
		if err != nil {
			http.Error(w, `{"error":"agent unreachable"}`, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}
