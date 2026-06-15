package router

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// HandleBrowser proxies the live-browser panel traffic to the caller's own
// container bridge (port 8088, NETWORK_MODE=host so it's reachable at the
// container host's internal_ip:8088, same IP as OpenCode's 5002).
//
// Routes (mounted under /api/browser):
//   GET  /api/browser/healthz        -> bridge /healthz   (HTTP)
//   POST /api/browser/browse         -> bridge /browse     (HTTP)
//   GET  /api/browser/ws/stream      -> bridge /ws/stream  (WebSocket)
//   GET  /api/browser/ws/control     -> bridge /ws/control (WebSocket)
//
// The user is resolved server-side from the session; a user can only ever
// reach their own container's bridge.
const bridgePort = 8088

func (r *Router) HandleBrowser(getUID func(*http.Request) int64) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		uid := getUID(req)
		if uid <= 0 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		// Resolve the user's container host IP (same lookup as the OpenCode proxy).
		var ip string
		err := r.pool.QueryRow(req.Context(), `
			SELECT h.internal_ip
			FROM containers c JOIN hosts h ON h.id = c.host_id
			WHERE c.user_id=$1`, uid).Scan(&ip)
		if err != nil {
			http.Error(w, `{"error":"no container for user"}`, http.StatusNotFound)
			return
		}
		target := fmt.Sprintf("%s:%d", ip, bridgePort)

		// Compute the bridge path: strip the /api/browser prefix.
		// e.g. /api/browser/ws/stream -> /ws/stream
		bridgePath := req.URL.Path
		const prefix = "/api/browser"
		if len(bridgePath) >= len(prefix) && bridgePath[:len(prefix)] == prefix {
			bridgePath = bridgePath[len(prefix):]
		}
		if bridgePath == "" {
			bridgePath = "/"
		}

		// WebSocket upgrade requests -> hijack + bidirectional pipe.
		if isWebSocketUpgrade(req) {
			proxyWebSocket(w, req, target, bridgePath)
			return
		}

		// Plain HTTP (healthz, browse) -> reverse proxy.
		targetURL := &url.URL{Scheme: "http", Host: target}
		rp := &httputil.ReverseProxy{
			Director: func(pr *http.Request) {
				pr.URL.Scheme = "http"
				pr.URL.Host = target
				pr.URL.Path = bridgePath
				pr.Host = target
			},
			FlushInterval: -1,
		}
		_ = targetURL
		rp.ServeHTTP(w, req)
	}
}

func isWebSocketUpgrade(req *http.Request) bool {
	conn := req.Header.Get("Connection")
	upg := req.Header.Get("Upgrade")
	return httpHeaderContainsToken(conn, "upgrade") &&
		(upg == "websocket" || upg == "WebSocket")
}

func httpHeaderContainsToken(header, token string) bool {
	// Connection may be "Upgrade", "keep-alive, Upgrade", etc.
	for _, part := range splitComma(header) {
		if equalFold(trimSpace(part), token) {
			return true
		}
	}
	return false
}

// proxyWebSocket dials the bridge, replays the client's upgrade request, and
// pipes bytes both ways until either side closes.
func proxyWebSocket(w http.ResponseWriter, req *http.Request, target, bridgePath string) {
	// Dial the bridge.
	backendConn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		http.Error(w, `{"error":"bridge unreachable"}`, http.StatusBadGateway)
		return
	}
	defer backendConn.Close()

	// Rewrite the request line to the bridge path, then write the raw upgrade
	// request to the backend so it performs the WS handshake with the client.
	req.URL.Scheme = "http"
	req.URL.Host = target
	req.URL.Path = bridgePath
	req.Host = target
	if err := req.Write(backendConn); err != nil {
		http.Error(w, `{"error":"bridge write failed"}`, http.StatusBadGateway)
		return
	}

	// Hijack the client connection.
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, `{"error":"hijack unsupported"}`, http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		http.Error(w, `{"error":"hijack failed"}`, http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	// Pipe both directions. Flush any buffered client bytes first.
	errc := make(chan error, 2)
	cp := func(dst io.Writer, src io.Reader) {
		_, e := io.Copy(dst, src)
		errc <- e
	}
	// backend -> client
	go cp(clientConn, backendConn)
	// client -> backend (drain the hijack buffer first if any)
	go func() {
		if clientBuf != nil && clientBuf.Reader.Buffered() > 0 {
			_, _ = io.CopyN(backendConn, clientBuf, int64(clientBuf.Reader.Buffered()))
		}
		cp(backendConn, clientConn)
	}()
	<-errc
}

// --- tiny string helpers (avoid importing strings just for these) ---
func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func trimSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	j := len(s)
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	return s[i:j]
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
