package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
)

// DebugLogHandler lets the browser (esp. mobile, where the console is invisible)
// POST {tag,msg} and have it appear in the server journal. Dev aid for tracing
// the voice loop on mobile.
func DebugLogHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Tag string `json:"tag"`
			Msg string `json:"msg"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in)
		log.Printf("[CLIENT] %s: %s", in.Tag, in.Msg)
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}
}
