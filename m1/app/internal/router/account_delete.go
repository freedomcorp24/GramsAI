// internal/router/account_delete.go
// Hard account deletion for the settings "Account" tab danger zone:
//   POST /account/delete
// Purges the user's container + data dir via the host agent, then deletes the
// users row. Every users(id) FK is ON DELETE CASCADE (sessions, usage_logs,
// payments, containers), so the row delete cleans up all child records.
// The container is purged FIRST; if that fails we abort without deleting, so a
// container is never orphaned (the row delete would cascade its registry entry).
package router

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5"
)

func acctDeleteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// HandleDeleteAccount permanently removes the caller's account.
func (r *Router) HandleDeleteAccount(getUID func(*http.Request) int64) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		uid := getUID(req)
		if uid <= 0 {
			acctDeleteJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		ctx := req.Context()

		// Purge the container + data dir first (if the user has one).
		var hostID int64
		err := r.pool.QueryRow(ctx, `SELECT host_id FROM containers WHERE user_id=$1`, uid).Scan(&hostID)
		switch {
		case err == nil && hostID > 0:
			if perr := r.agentPurge(ctx, hostID, uid); perr != nil {
				log.Printf("delete-account: purge user %d failed: %v", uid, perr)
				acctDeleteJSON(w, 500, map[string]string{"error": "could not remove your container, please try again"})
				return
			}
		case errors.Is(err, pgx.ErrNoRows):
			// No container provisioned — nothing to purge.
		case err != nil:
			log.Printf("delete-account: container lookup user %d failed: %v", uid, err)
			acctDeleteJSON(w, 500, map[string]string{"error": "server error"})
			return
		}

		// Delete the account row; FK cascade removes sessions, usage_logs,
		// payments, and the containers registry entry.
		if _, derr := r.pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid); derr != nil {
			log.Printf("delete-account: delete user %d failed: %v", uid, derr)
			acctDeleteJSON(w, 500, map[string]string{"error": "server error"})
			return
		}

		// Clear the session cookie so the browser is logged out immediately.
		http.SetCookie(w, &http.Cookie{
			Name: "gramsai_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		})
		acctDeleteJSON(w, 200, map[string]any{"ok": true, "deleted": true})
	}
}
