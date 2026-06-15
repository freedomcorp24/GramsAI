// GramsAI control-agent — runs ON each worker host (M2 today).
// Accepts authenticated commands from the gateway and runs docker LOCALLY.
package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"path/filepath"
	"strings"
	"time"
)

var (
	secret    string
	image     = envOr("GRAMSAI_IMAGE", "gramsai/opencode:latest")
	dataRoot  = envOr("GRAMSAI_DATA", "/data/opencode")
	gatewayV1 = envOr("GRAMSAI_GATEWAY_V1", "http://10.152.152.111/v1")
	dnsServer = envOr("GRAMSAI_DNS", "10.152.152.10")
	basePort  = envInt("GRAMSAI_BASE_PORT", 5002)
)

func main() {
	secret = os.Getenv("AGENT_SECRET")
	if secret == "" {
		log.Fatal("AGENT_SECRET is required")
	}
	listen := envOr("AGENT_LISTEN", "0.0.0.0:9090")

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/spawn", authed(handleSpawn))
	mux.HandleFunc("/stop", authed(handleStop))
	mux.HandleFunc("/status", authed(handleStatus))
	mux.HandleFunc("/recreate", authed(handleRecreate))
	mux.HandleFunc("/usage", authed(handleUsage))
	mux.HandleFunc("/purge", authed(handlePurge))
	mux.HandleFunc("/wipe-chats", authed(handleWipeChats))

	log.Printf("control-agent listening on %s (image=%s)", listen, image)
	srv := &http.Server{Addr: listen, Handler: mux, ReadTimeout: 30 * time.Second, WriteTimeout: 120 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Agent-Secret")
		if subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		next(w, r)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "image": image})
}

type spawnReq struct {
	UserID int64  `json:"user_id"`
	Token  string `json:"token"`
	Port   int    `json:"port"`
	DEK    string `json:"dek"`   // GRAMSAI_DEK_AGENT: base64 per-user DEK (may be empty)
}

func handleSpawn(w http.ResponseWriter, r *http.Request) {
	var b spawnReq
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.UserID == 0 || b.Token == "" || b.Port == 0 {
		writeJSON(w, 400, map[string]any{"error": "user_id, token, port required"})
		return
	}
	name := containerName(b.UserID)
	if running(name) {
		writeJSON(w, 200, map[string]any{"ok": true, "name": name, "port": b.Port, "status": "running", "reused": true})
		return
	}
	_ = run("docker", "rm", "-f", name)
	if err := ensureDirs(b.UserID); err != nil {
		writeJSON(w, 500, map[string]any{"error": "mkdir: " + err.Error()})
		return
	}
	ws := fmt.Sprintf("%s/user-%d/workspace", dataRoot, b.UserID)
	cfg := fmt.Sprintf("%s/user-%d/config", dataRoot, b.UserID)
	loc := fmt.Sprintf("%s/user-%d/local", dataRoot, b.UserID)
	portStr := strconv.Itoa(b.Port)
	args := []string{
		"run", "-d", "--name", name, "--network", "gramsai-iso", "-w", "/workspace",
		"--shm-size=512m",
		"-p", "10.152.152.100:" + portStr + ":" + portStr,
		"-p", "10.152.152.100:8088:8088",
		"-v", ws + ":/workspace", "-v", loc + ":/root/.local", "-v", cfg + ":/root/.config",
		"-e", "GRAMSAI_TOKEN=" + b.Token, "-e", "GRAMSAI_DEK=" + b.DEK, "-e", "GRAMSAI_WORKSPACE=/workspace",
		"-e", "GRAMSAI_GATEWAY_V1=" + gatewayV1, "--dns", dnsServer, "--restart=always",
		image, "serve", "--hostname", "0.0.0.0", "--port", portStr,
	}
	if out, err := runOut("docker", args...); err != nil {
		writeJSON(w, 500, map[string]any{"error": "docker run failed", "detail": out})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "name": name, "port": b.Port, "status": "running"})
}

func handleStop(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UserID int64 `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.UserID == 0 {
		writeJSON(w, 400, map[string]any{"error": "user_id required"})
		return
	}
	name := containerName(b.UserID)
	_ = run("docker", "rm", "-f", name)
	writeJSON(w, 200, map[string]any{"ok": true, "name": name, "status": "stopped"})
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	uid := r.URL.Query().Get("user_id")
	if uid == "" {
		writeJSON(w, 400, map[string]any{"error": "user_id required"})
		return
	}
	id, _ := strconv.ParseInt(uid, 10, 64)
	name := containerName(id)
	writeJSON(w, 200, map[string]any{"name": name, "running": running(name)})
}

func handleUsage(w http.ResponseWriter, r *http.Request) {
	uid := r.URL.Query().Get("user_id")
	if uid == "" {
		writeJSON(w, 400, map[string]any{"error": "user_id required"})
		return
	}
	id, err := strconv.ParseInt(uid, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 400, map[string]any{"error": "bad user_id"})
		return
	}
	dir := fmt.Sprintf("%s/user-%d", dataRoot, id)
	if _, statErr := os.Stat(dir); statErr != nil {
		writeJSON(w, 200, map[string]any{"user_id": id, "bytes": 0})
		return
	}
	out, runErr := runOut("du", "-sb", dir)
	if runErr != nil {
		if outK, kErr := runOut("du", "-sk", dir); kErr == nil {
			if f := strings.Fields(outK); len(f) > 0 {
				if kib, perr := strconv.ParseInt(f[0], 10, 64); perr == nil {
					writeJSON(w, 200, map[string]any{"user_id": id, "bytes": kib * 1024})
					return
				}
			}
		}
		writeJSON(w, 500, map[string]any{"error": "du failed", "detail": out})
		return
	}
	var bytes int64
	if f := strings.Fields(out); len(f) > 0 {
		bytes, _ = strconv.ParseInt(f[0], 10, 64)
	}
	writeJSON(w, 200, map[string]any{"user_id": id, "bytes": bytes})
}

// handlePurge tears down a user's container AND deletes their data directory.
// IRREVERSIBLE. Used by the gateway lifecycle job after the 7-day grace period.
// Hard guard on user_id so a malformed/zero id can never expand the rm path to
// the parent dir (rm -rf /data/opencode/user-  would be catastrophic).
func handlePurge(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UserID int64 `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.UserID <= 0 {
		writeJSON(w, 400, map[string]any{"error": "valid positive user_id required"})
		return
	}
	name := containerName(b.UserID)
	_ = run("docker", "rm", "-f", name)
	dir := fmt.Sprintf("%s/user-%d", dataRoot, b.UserID)
	// Final belt-and-braces: never operate on a path that doesn't end in /user-N.
	if !strings.HasSuffix(dir, fmt.Sprintf("/user-%d", b.UserID)) || b.UserID <= 0 {
		writeJSON(w, 400, map[string]any{"error": "refusing unsafe path"})
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		writeJSON(w, 500, map[string]any{"error": "purge failed", "detail": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "user_id": b.UserID, "purged": dir})
}

func handleWipeChats(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UserID int64 `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.UserID <= 0 {
		writeJSON(w, 400, map[string]any{"error": "valid positive user_id required"})
		return
	}
	// Remove ONLY the opencode SQLite db files (chats), preserving workspace + config.
	localDir := fmt.Sprintf("%s/user-%d/local/share/opencode", dataRoot, b.UserID)
	if !strings.Contains(localDir, fmt.Sprintf("/user-%d/", b.UserID)) || b.UserID <= 0 {
		writeJSON(w, 400, map[string]any{"error": "refusing unsafe path"})
		return
	}
	// Stop the container so SQLite isn't mid-write, wipe db files, leave the rest.
	name := containerName(b.UserID)
	_ = run("docker", "rm", "-f", name)
	matches, _ := filepath.Glob(localDir + "/opencode*.db*")
	for _, f := range matches {
		_ = os.Remove(f)
	}
	// Also clear derived storage/tool-output dirs that hold conversation artifacts.
	_ = os.RemoveAll(fmt.Sprintf("%s/user-%d/local/share/opencode/storage", dataRoot, b.UserID))
	_ = os.RemoveAll(fmt.Sprintf("%s/user-%d/local/share/opencode/tool-output", dataRoot, b.UserID))
	writeJSON(w, 200, map[string]any{"ok": true, "user_id": b.UserID, "wiped": len(matches)})
}

func handleRecreate(w http.ResponseWriter, r *http.Request) {
	var b spawnReq
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.UserID == 0 || b.Token == "" || b.Port == 0 {
		writeJSON(w, 400, map[string]any{"error": "user_id, token, port required"})
		return
	}
	name := containerName(b.UserID)
	_ = run("docker", "rm", "-f", name)
	handleSpawnInline(w, b, name)
}

func handleSpawnInline(w http.ResponseWriter, b spawnReq, name string) {
	if err := ensureDirs(b.UserID); err != nil {
		writeJSON(w, 500, map[string]any{"error": "mkdir: " + err.Error()})
		return
	}
	ws := fmt.Sprintf("%s/user-%d/workspace", dataRoot, b.UserID)
	cfg := fmt.Sprintf("%s/user-%d/config", dataRoot, b.UserID)
	loc := fmt.Sprintf("%s/user-%d/local", dataRoot, b.UserID)
	portStr := strconv.Itoa(b.Port)
	args := []string{
		"run", "-d", "--name", name, "--network", "gramsai-iso", "-w", "/workspace",
		"--shm-size=512m",
		"-p", "10.152.152.100:" + portStr + ":" + portStr,
		"-p", "10.152.152.100:8088:8088",
		"-v", ws + ":/workspace", "-v", loc + ":/root/.local", "-v", cfg + ":/root/.config",
		"-e", "GRAMSAI_TOKEN=" + b.Token, "-e", "GRAMSAI_DEK=" + b.DEK, "-e", "GRAMSAI_WORKSPACE=/workspace",
		"-e", "GRAMSAI_GATEWAY_V1=" + gatewayV1, "--dns", dnsServer, "--restart=always",
		image, "serve", "--hostname", "0.0.0.0", "--port", portStr,
	}
	if out, err := runOut("docker", args...); err != nil {
		writeJSON(w, 500, map[string]any{"error": "docker run failed", "detail": out})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "name": name, "port": b.Port, "status": "running"})
}

func containerName(userID int64) string { return fmt.Sprintf("oc-user-%d", userID) }

func ensureDirs(userID int64) error {
	for _, sub := range []string{"workspace", "config", "local"} {
		if err := os.MkdirAll(fmt.Sprintf("%s/user-%d/%s", dataRoot, userID, sub), 0755); err != nil {
			return err
		}
	}
	return nil
}

func running(name string) bool {
	out, err := runOut("docker", "inspect", "-f", "{{.State.Running}}", name)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "true"
}

func run(name string, args ...string) error { return exec.Command(name, args...).Run() }
func runOut(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return d
}
