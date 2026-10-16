package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Request-size caps. Most bodies are tiny JSON commands; manifests, edit
// forms and helm values documents can legitimately be large.
const (
	maxBodySmall = 64 << 10 // 64 KiB: delete/scale/ai/passthrough/…
	maxBodyYAML  = 5 << 20  // 5 MiB: apply, edit, config, helm install/upgrade
)

// Connection timeouts. WriteTimeout is a ceiling for ordinary request/
// response handlers (a helm --wait install can legitimately run for minutes);
// the streaming endpoints (SSE logs/watch, WebSocket exec) clear their own
// deadlines via clearStreamDeadlines, after the request body has been read.
var (
	serverReadHeaderTimeout = 10 * time.Second
	serverReadTimeout       = 60 * time.Second
	serverWriteTimeout      = 10 * time.Minute
	serverIdleTimeout       = 2 * time.Minute
)

// bodyLimitFor picks the request-size cap for a URL path.
func bodyLimitFor(path string) int64 {
	switch {
	case strings.HasPrefix(path, "/api/v1/apply/"),
		path == "/api/v1/config",
		strings.HasPrefix(path, "/api/v1/helm/upgrade/"),
		strings.HasPrefix(path, "/api/v1/helm/install/"),
		strings.HasPrefix(path, "/api/v1/resources/") && strings.Contains(path, "/edit/"):
		return maxBodyYAML
	}
	return maxBodySmall
}

// limitBody rejects oversized request bodies up front (413 when the declared
// Content-Length is already too big) and caps chunked/unknown-length bodies
// with http.MaxBytesReader so a handler can never buffer more than the limit.
func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) || r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		limit := bodyLimitFor(r.URL.Path)
		if r.ContentLength > limit {
			s.render.Error(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// decodeJSON decodes the request body into dst. The body is capped even when
// a handler is invoked without the middleware chain.
func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimitFor(r.URL.Path))
	return json.NewDecoder(r.Body).Decode(dst)
}

// clearStreamDeadlines lifts the server-wide read and write deadlines for a
// long-lived stream (SSE / WebSocket). net/http applies Server.ReadTimeout and
// WriteTimeout to the whole connection, which would cut every log tail, AI
// answer and exec session. Handlers call it only AFTER reading the request
// body (so a slow-body client still hits the read deadline); a stale read
// deadline must also be cleared because net/http's background read treats its
// expiry as a client disconnect and cancels the request context.
func clearStreamDeadlines(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
}

// newHTTPServer builds the http.Server with all hardening timeouts applied.
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
	}
}
