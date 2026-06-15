package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// HandleDownload streams a zip of the caller's chat folder. It resolves the
// session to a user, finds that user's host control-agent, and proxies the
// agent's /dl (which zips /data/opencode/user-N/workspace[/dir] on the host).
// The session is resolved server-side, so a user can only download their own
// workspace regardless of the dir they pass.
func (r *Router) HandleDownload(getUID func(*http.Request) int64) http.HandlerFunc {
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
		// GRAMSAI_DL_BY_CHAT: resolve the worktree dir from a chat id so the
		// browser never sends an internal path. Overrides any 'dir' param.
		if chat := req.URL.Query().Get("chat"); chat != "" {
			wt, derr := r.chatDirectory(req, uid, chat)
			if derr != nil || wt == "" {
				http.Error(w, `{"error":"chat not found"}`, http.StatusNotFound)
				return
			}
			dir = wt
			if fn := req.URL.Query().Get("file"); fn != "" {
				dir = strings.TrimRight(dir, "/") + "/" + path.Base(fn)
			}
		}
		agentURL := fmt.Sprintf("%s/dl?user_id=%d&dir=%s", controlURL, uid, url.QueryEscape(dir))
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
		if resp.StatusCode != http.StatusOK {
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
			return
		}
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		if cd := resp.Header.Get("Content-Disposition"); cd != "" {
			w.Header().Set("Content-Disposition", cd)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, resp.Body)
	}
}

// chatDirectory asks the user's running container for a session's working
// directory (the worktree path). Returns "" if the container or session is not
// found. Reuses the same container lookup as the OpenCode proxy.
func (r *Router) chatDirectory(req *http.Request, uid int64, chatID string) (string, error) {
	var ip, name, status string
	var port int
	var hostID int64
	err := r.pool.QueryRow(req.Context(), `
		SELECT c.host_id, h.internal_ip, c.port, c.container_name, c.status
		FROM containers c JOIN hosts h ON h.id = c.host_id
		WHERE c.user_id=$1`, uid).Scan(&hostID, &ip, &port, &name, &status)
	if err != nil {
		return "", err
	}
	api := fmt.Sprintf("http://%s:%d/session/%s", ip, port, url.PathEscape(chatID))
	creq, err := http.NewRequestWithContext(req.Context(), "GET", api, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(creq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("session lookup status %d", resp.StatusCode)
	}
	var sess struct {
		Directory string `json:"directory"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		return "", err
	}
	return sess.Directory, nil
}
