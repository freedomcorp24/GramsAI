package proxy

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"net/http/httputil"
	"net/url"

	"gramsai/internal/containers"
)

type SSEProxy struct {
	manager *containers.Manager
}

func NewSSEProxy(mgr *containers.Manager) *SSEProxy {
	return &SSEProxy{manager: mgr}
}

func (p *SSEProxy) HandleProxy(w http.ResponseWriter, r *http.Request) {
	userID := int64(1)

	c, err := p.manager.GetOrCreate(r.Context(), userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("container error: %v", err), http.StatusInternalServerError)
		return
	}

	targetURL := fmt.Sprintf("http://%s:%d", p.manager.DockerHostIP(), c.Port)
	target, err := url.Parse(targetURL)
	if err != nil {
		http.Error(w, "bad target url", http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1

	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		req.Host = target.Host
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/oc")
		if req.URL.Path == "" { req.URL.Path = "/" }
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("X-Frame-Options")
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error: %v", err)
		http.Error(w, "container unavailable", http.StatusBadGateway)
	}

	log.Printf("Proxying to %s (user=%d)", targetURL, userID)
	proxy.ServeHTTP(w, r)
}

func (p *SSEProxy) HandleSSE(w http.ResponseWriter, r *http.Request) {
	userID := int64(1)

	c, err := p.manager.GetOrCreate(r.Context(), userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("container error: %v", err), http.StatusInternalServerError)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/oc")
	if path == "" { path = "/" }
	targetURL := fmt.Sprintf("http://%s:%d%s", p.manager.DockerHostIP(), c.Port, path)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "request error", http.StatusInternalServerError)
		return
	}

	for k, vv := range r.Header {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "container unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	flusher, ok := w.(http.Flusher)
	if !ok {
		io.Copy(w, resp.Body)
		return
	}

	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			break
		}
	}
}
