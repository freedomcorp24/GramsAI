package router

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
)

// HandleUpload streams an uploaded file to the caller's container workspace.
// It resolves the caller to a user, finds that user's host control-agent, and
// proxies the agent's /upload (which writes /data/opencode/user-N/workspace/
// uploads/<name> on the host). The user is resolved server-side, so a caller
// can only write into their own workspace. Returns the agent JSON, which
// includes the container-relative path ("/workspace/uploads/<name>").
func (r *Router) HandleUpload(getUID func(*http.Request) int64) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		uid := getUID(req)
		if uid <= 0 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		name := path.Base(req.URL.Query().Get("name"))
		if name == "" || name == "." || name == "/" {
			http.Error(w, `{"error":"valid name required"}`, http.StatusBadRequest)
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
		agentURL := fmt.Sprintf("%s/upload?user_id=%d&name=%s", controlURL, uid, url.QueryEscape(name))
		areq, err := http.NewRequestWithContext(req.Context(), "POST", agentURL, req.Body)
		if err != nil {
			http.Error(w, `{"error":"bad agent request"}`, http.StatusInternalServerError)
			return
		}
		areq.Header.Set("X-Agent-Secret", r.agentSecret)
		areq.Header.Set("Content-Type", "application/octet-stream")
		if req.ContentLength > 0 {
			areq.ContentLength = req.ContentLength
		}
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
