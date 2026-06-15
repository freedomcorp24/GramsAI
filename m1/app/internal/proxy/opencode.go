// internal/proxy/opencode.go
// Reverse proxy /oc/* to OpenCode container on Machine 2.
// Handles SSE streaming with proper flush behavior.
package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// NewOpenCodeProxy creates a reverse proxy to the OpenCode container.
// opencodeURL should be like "http://10.152.152.100:5002"
func NewOpenCodeProxy(opencodeURL string) http.Handler {
	target, err := url.Parse(opencodeURL)
	if err != nil {
		log.Fatalf("Invalid OPENCODE_URL: %v", err)
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Strip /oc prefix
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = strings.TrimPrefix(req.URL.Path, "/oc")
			if req.URL.Path == "" {
				req.URL.Path = "/"
			}
			req.Host = target.Host

			// Remove hop-by-hop headers
			req.Header.Del("Connection")
			req.Header.Del("Upgrade")
		},
		// FlushInterval -1 means flush immediately — critical for SSE
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			// Add SSE-friendly headers
			if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
				resp.Header.Set("X-Accel-Buffering", "no")
				resp.Header.Set("Cache-Control", "no-cache")
			}
			return nil
		},
	}

	return proxy
}
