// GramsAI control-agent — runs ON each worker host (M2 today).
// Accepts authenticated commands from the gateway and runs docker LOCALLY.
// Docker is NEVER exposed to the network; only these few operations are.
// Stdlib only (no external deps) so it builds anywhere with `go build`.
package main

import (
	"archive/zip"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	secret    string
	image     = envOr("GRAMSAI_IMAGE", "gramsai/opencode:latest")
	dataRoot  = envOr("GRAMSAI_DATA", "/data/opencode")
	gatewayV1 = envOr("GRAMSAI_GATEWAY_V1", "http://10.152.152.111/v1") // containers call gateway for LLM
	dnsServer = envOr("GRAMSAI_DNS", "10.152.152.10")                   // Whonix gateway DNS
	basePort  = envInt("GRAMSAI_BASE_PORT", 5002)
)

func main() {
	secret = os.Getenv("AGENT_SECRET")
	if secret == "" {
		log.Fatal("AGENT_SECRET is required")
	}
	listen := envOr("AGENT_LISTEN", "0.0.0.0:9090")

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth) // unauthenticated liveness
	mux.HandleFunc("/spawn", authed(handleSpawn))
	mux.HandleFunc("/stop", authed(handleStop))
	mux.HandleFunc("/status", authed(handleStatus))
	mux.HandleFunc("/recreate", authed(handleRecreate))
	mux.HandleFunc("/dl", authed(handleDownload))
	mux.HandleFunc("/usage", authed(handleUsage)) // disk usage for a user's data dir

	log.Printf("control-agent listening on %s (image=%s)", listen, image)
	srv := &http.Server{Addr: listen, Handler: mux, ReadTimeout: 30 * time.Second, WriteTimeout: 120 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// ---- auth ----

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

// ---- spawn ----

type spawnReq struct {
	UserID int64  `json:"user_id"`
	Token  string `json:"token"` // the user's gsk- gateway token
	Port   int    `json:"port"`  // gateway assigns; agent honors it
}

func handleSpawn(w http.ResponseWriter, r *http.Request) {
	var b spawnReq
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.UserID == 0 || b.Token == "" || b.Port == 0 {
		writeJSON(w, 400, map[string]any{"error": "user_id, token, port required"})
		return
	}
	name := containerName(b.UserID)

	// If already running, just report it (idempotent).
	if running(name) {
		writeJSON(w, 200, map[string]any{"ok": true, "name": name, "port": b.Port, "status": "running", "reused": true})
		return
	}
	// Clean any dead container with the same name.
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
		"run", "-d", "--name", name, "--network", "host", "-w", "/workspace",
		"-v", ws + ":/workspace",
		"-v", loc + ":/root/.local",
		"-v", cfg + ":/root/.config",
		"-e", "GRAMSAI_TOKEN=" + b.Token,
		"-e", "GRAMSAI_WORKSPACE=/workspace",
		"-e", "GRAMSAI_GATEWAY_V1=" + gatewayV1,
		"--dns", dnsServer,
		"--restart=always",
		image,
		"serve", "--hostname", "0.0.0.0", "--port", portStr,
	}
	if out, err := runOut("docker", args...); err != nil {
		writeJSON(w, 500, map[string]any{"error": "docker run failed", "detail": out})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "name": name, "port": b.Port, "status": "running"})
}

// ---- stop ----

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

// ---- status ----

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

// ---- usage (disk) ----
// Returns total bytes used by /data/opencode/user-N (workspace + config + local).
// Used by the gateway's background poller to enforce per-tier storage quotas.
// du -sb gives apparent size in bytes; if the dir doesn't exist yet, returns 0.
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
		// Never provisioned (or already torn down) -> zero usage, not an error.
		writeJSON(w, 200, map[string]any{"user_id": id, "bytes": 0})
		return
	}
	out, runErr := runOut("du", "-sb", dir)
	if runErr != nil {
		// Fallback for hosts without GNU du -b: use -sk (KiB) and scale.
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

// ---- recreate (image update) ----

func handleRecreate(w http.ResponseWriter, r *http.Request) {
	var b spawnReq
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.UserID == 0 || b.Token == "" || b.Port == 0 {
		writeJSON(w, 400, map[string]any{"error": "user_id, token, port required"})
		return
	}
	name := containerName(b.UserID)
	_ = run("docker", "rm", "-f", name)
	// re-spawn via the same path
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
		"run", "-d", "--name", name, "--network", "host", "-w", "/workspace",
		"-v", ws + ":/workspace", "-v", loc + ":/root/.local", "-v", cfg + ":/root/.config",
		"-e", "GRAMSAI_TOKEN=" + b.Token, "-e", "GRAMSAI_WORKSPACE=/workspace",
		"-e", "GRAMSAI_GATEWAY_V1=" + gatewayV1, "--dns", dnsServer, "--restart=always",
		image, "serve", "--hostname", "0.0.0.0", "--port", portStr,
	}
	if out, err := runOut("docker", args...); err != nil {
		writeJSON(w, 500, map[string]any{"error": "docker run failed", "detail": out})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "name": name, "port": b.Port, "status": "running"})
}

// ---- helpers ----

// handleDownload streams a zip of a user's chat folder. The gateway calls this
// with ?user_id=N&dir=/workspace/<sub>. Host path is /data/opencode/user-N/
// workspace[/<sub>]; any path escaping the user's workspace is refused.
func handleDownload(w http.ResponseWriter, r *http.Request) {
	uid, _ := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	if uid <= 0 {
		writeJSON(w, 400, map[string]any{"error": "valid positive user_id required"})
		return
	}
	userRoot := fmt.Sprintf("%s/user-%d", dataRoot, uid)
	cdir := r.URL.Query().Get("dir")
	var target string
	switch {
	case cdir == "/workspace" || strings.HasPrefix(cdir, "/workspace/"):
		target = userRoot + "/workspace" + strings.TrimPrefix(cdir, "/workspace")
	case strings.HasPrefix(cdir, "/root/.local"):
		target = userRoot + "/local" + strings.TrimPrefix(cdir, "/root/.local")
	case strings.HasPrefix(cdir, "/root/.config"):
		target = userRoot + "/config" + strings.TrimPrefix(cdir, "/root/.config")
	default:
		writeJSON(w, 400, map[string]any{"error": "unsupported dir"})
		return
	}
	target = filepath.Clean(target)
	if target != userRoot && !strings.HasPrefix(target, userRoot+"/") {
		writeJSON(w, 400, map[string]any{"error": "refusing unsafe path"})
		return
	}
	if r.URL.Query().Get("mode") == "ls" {
		type fileEnt struct {
			Path string `json:"path"`
			Name string `json:"name"`
			Size int64  `json:"size"`
		}
		files := []fileEnt{}
		_ = filepath.Walk(target, func(p string, fi os.FileInfo, werr error) error {
			if werr != nil || fi.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(target, p)
			if rerr != nil {
				return nil
			}
			files = append(files, fileEnt{
				Path: strings.TrimRight(cdir, "/") + "/" + filepath.ToSlash(rel),
				Name: filepath.Base(p),
				Size: fi.Size(),
			})
			return nil
		})
		writeJSON(w, 200, map[string]any{"files": files})
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	if !info.IsDir() {
		f, err := os.Open(target)
		if err != nil {
			writeJSON(w, 404, map[string]any{"error": "not found"})
			return
		}
		defer f.Close()
		disp := "attachment"
		if r.URL.Query().Get("disp") == "inline" {
			disp = "inline"
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disp, filepath.Base(target)))
		http.ServeContent(w, r, filepath.Base(target), info.ModTime(), f)
		return
	}
	name := filepath.Base(target)
	if name == "workspace" || name == "" || name == "." || name == string(filepath.Separator) {
		name = "chat"
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".zip"))
	zw := zip.NewWriter(w)
	defer zw.Close()
	_ = filepath.Walk(target, func(p string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil || fi.IsDir() {
			return nil
		}
		relName, err := filepath.Rel(target, p)
		if err != nil {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		hw, err := zw.Create(relName)
		if err != nil {
			f.Close()
			return nil
		}
		_, _ = io.Copy(hw, f)
		f.Close()
		return nil
	})
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
