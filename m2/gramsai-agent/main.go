// GramsAI control-agent — runs ON each worker host (M2 today).
// Accepts authenticated commands from the gateway and runs docker LOCALLY.
package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
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
	mux.HandleFunc("/dl", authed(handleDownload))

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
	BrowserPort int `json:"browser_port"`
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
		"-p", "10.152.152.100:" + strconv.Itoa(b.BrowserPort) + ":8088",
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
	// Count ONLY real user-generated storage against the quota:
	//   local/    = chats DB, snapshots, worktrees (user data)
	//   workspace = user files, EXCLUDING the .git baseline (system overhead)
	// We deliberately EXCLUDE config/ (the identical 14 seeded agent files every
	// user gets — not user data, must not consume their 1GB).
	var bytes int64
	// local/ (full)
	if out, e := runOut("du", "-sb", dir+"/local"); e == nil {
		if f := strings.Fields(out); len(f) > 0 {
			if v, pe := strconv.ParseInt(f[0], 10, 64); pe == nil { bytes += v }
		}
	}
	// workspace/ minus .git: du -sb --exclude=.git
	if out, e := runOut("du", "-sb", "--exclude=.git", dir+"/workspace"); e == nil {
		if f := strings.Fields(out); len(f) > 0 {
			if v, pe := strconv.ParseInt(f[0], 10, 64); pe == nil { bytes += v }
		}
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
	// Reclaim ALL session worktrees (per-chat checkouts that opencode never tears
	// down). Container is already removed above, so these are just host files.
	_ = os.RemoveAll(fmt.Sprintf("%s/user-%d/local/share/opencode/worktree", dataRoot, b.UserID))
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
		"-p", "10.152.152.100:" + strconv.Itoa(b.BrowserPort) + ":8088",
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


// handleDownload serves a single file from a user's workspace, bind-mounted on
// the host at {dataRoot}/user-N/workspace. Gateway passes user_id + dir (a
// /workspace-relative or /workspace-prefixed path). Images served inline so they
// render in <img>; everything else as attachment. Traversal-guarded.

func serveFile(w http.ResponseWriter, full string) {
	f, err := os.Open(full)
	if err != nil { writeJSON(w, 404, map[string]any{"error":"file not found"}); return }
	defer f.Close()
	ctype := mime.TypeByExtension(strings.ToLower(filepath.Ext(full)))
	if ctype == "" { ctype = "application/octet-stream" }
	w.Header().Set("Content-Type", ctype)
	if strings.HasPrefix(ctype, "image/") {
		w.Header().Set("Content-Disposition", `inline; filename="`+filepath.Base(full)+`"`)
	} else {
		w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(full)+`"`)
	}
	w.WriteHeader(200)
	_, _ = io.Copy(w, f)
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	uidStr := r.URL.Query().Get("user_id")
	id, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 400, map[string]any{"error": "valid user_id required"})
		return
	}
	userRoot := fmt.Sprintf("%s/user-%d", dataRoot, id)
	base := userRoot + "/workspace"
	rel := r.URL.Query().Get("dir")
	// Worktree paths (/root/.local/share/opencode/worktree/<hash>/<sub>) map to the
	// user's local mount; /workspace paths map to the workspace mount.
	if strings.HasPrefix(rel, "/root/.local") {
		base = userRoot + "/local" + strings.TrimPrefix(rel, "/root/.local")
		rel = ""
		// base now points at the exact file/dir; treat trailing as full path
		if fi, e := os.Stat(base); e == nil && !fi.IsDir() {
			serveFile(w, base); return
		}
	}
	rel = strings.TrimPrefix(rel, "/workspace")
	rel = strings.TrimPrefix(rel, "/")

	if rel == "" {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="workspace.tar.gz"`)
		cmd := exec.Command("tar", "-czf", "-",
			"--exclude=.git", "--exclude=.config", "--exclude=.local",
			"--exclude=*.db", "--exclude=*.db-shm", "--exclude=*.db-wal",
			"-C", base, ".")
		cmd.Stdout = w
		_ = cmd.Run()
		return
	}

	full := filepath.Join(base, rel)
	cleanBase := filepath.Clean(base)
	cleanFull := filepath.Clean(full)
	if cleanFull != cleanBase && !strings.HasPrefix(cleanFull, cleanBase+string(os.PathSeparator)) {
		writeJSON(w, 400, map[string]any{"error": "invalid path"})
		return
	}
	fi, statErr := os.Stat(cleanFull)
	if statErr != nil || fi.IsDir() {
		writeJSON(w, 404, map[string]any{"error": "file not found"})
		return
	}
	f, openErr := os.Open(cleanFull)
	if openErr != nil {
		writeJSON(w, 404, map[string]any{"error": "file not found"})
		return
	}
	defer f.Close()

	ext := strings.ToLower(filepath.Ext(cleanFull))
	ctype := mime.TypeByExtension(ext)
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	if strings.HasPrefix(ctype, "image/") {
		w.Header().Set("Content-Disposition", "inline; filename=\""+filepath.Base(cleanFull)+"\"")
	} else {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(cleanFull)+"\"")
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.WriteHeader(200)
	_, _ = io.Copy(w, f)
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
