package web

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// middleware is the outermost chain wrapping the mux. Order matters: recover
// must run last (outermost) so a panic in any inner layer is caught, while
// DNS-rebind / token / CSRF must run before the route handler observes the
// request.
func (s *Server) middleware(next http.Handler) http.Handler {
	return s.recoverPanic(
		s.logRequests(
			s.enforceHost(
				s.requireToken(
					s.csrfGuard(next),
				),
			),
		),
	)
}

// recoverPanic turns any panic into a 500 with no stack leakage to the user.
// We log the stack to stderr so the operator running `sk web` can diagnose it.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				fmt.Fprintf(unwrapStderr(), "sk web panic: %v\n%s\n", rec, debug.Stack())
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// logRequests writes a compact access line per request. We deliberately keep
// it terse — verbose logs would drown the operator running `sk web`.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, code: 200}
		next.ServeHTTP(rw, r)
		// Skip the static-asset firehose unless verbose.
		if strings.HasPrefix(r.URL.Path, "/static/") {
			return
		}
		fmt.Fprintf(unwrapStderr(), "%s %s %d %dms\n",
			r.Method, r.URL.Path, rw.code, time.Since(start).Milliseconds())
	})
}

// enforceHost rejects requests whose Host header doesn't match an allowed
// value. This is the DNS-rebind defense: a browser tab loaded from
// http://evil.example pointed at 127.0.0.1 (via rebound DNS) cannot trick the
// server into accepting its requests because its tab still sends Host:
// evil.example.
func (s *Server) enforceHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		host = strings.Trim(host, "[]")
		for _, ok := range s.allowedHosts() {
			h := strings.Trim(ok, "[]")
			if host == h {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "forbidden host", http.StatusForbidden)
	})
}

// requireToken enforces s.cfg.Token if one is set. The token can arrive via
// ?token=... (initial page load) or via the X-Superkube-Token header (HTMX
// requests after the page picks it up from the query string).
func (s *Server) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token == "" {
			next.ServeHTTP(w, r)
			return
		}
		// Asset paths are public — they leak nothing sensitive.
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		got := r.URL.Query().Get("token")
		if got == "" {
			got = r.Header.Get("X-Superkube-Token")
		}
		if got == "" {
			if c, err := r.Cookie("sk_token"); err == nil {
				got = c.Value
			}
		}
		if got != s.cfg.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Persist the token in a cookie so XHR follow-ups don't need to carry
		// it on every URL — cleaner than dragging it across HTMX swaps.
		http.SetCookie(w, &http.Cookie{
			Name:     "sk_token",
			Value:    s.cfg.Token,
			Path:     "/",
			SameSite: http.SameSiteStrictMode,
		})
		next.ServeHTTP(w, r)
	})
}

// csrfGuard ensures state-changing requests carry the CSRF cookie+header pair.
// The cookie is set by the page handler; HTMX is configured (in app.js) to
// echo it in X-CSRF-Token on every non-GET request. WebSocket upgrades carry
// the cookie automatically and additionally check Origin in the ws handler.
func (s *Server) csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		// /ws/* is upgraded immediately; CSRF for WebSocket is enforced inside
		// the ws handler via Origin checks (coder/websocket does this when we
		// don't pass InsecureSkipVerify).
		if strings.HasPrefix(r.URL.Path, "/ws/") {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(csrfCookieName)
		if err != nil || cookie.Value == "" {
			http.Error(w, "missing csrf cookie", http.StatusForbidden)
			return
		}
		header := r.Header.Get("X-CSRF-Token")
		if header == "" {
			header = r.URL.Query().Get("csrf")
		}
		if header != cookie.Value {
			http.Error(w, "csrf token mismatch", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying ResponseWriter's Flush so SSE handlers
// can stream events through our middleware chain. Without this, the type
// assertion `w.(http.Flusher)` in sse.go would fail — Go promotes only the
// methods declared in the embedded interface (http.ResponseWriter), and
// Flush is not one of them.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying ResponseWriter so WebSocket upgrades
// keep working through the middleware chain. coder/websocket's Accept does
// not require Hijack on Go 1.20+, but other libraries (and HTTP/1.1 fallback
// paths) do; defining it costs nothing.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errNoHijack
}

var errNoHijack = errors.New("response writer does not support hijack")

// unwrapStderr is split out so tests can swap log destination.
var unwrapStderr = func() interface {
	Write(p []byte) (int, error)
} {
	return stderr
}

// stderr is the package-private destination for log lines. Indirection allows
// tests to redirect it.
var stderr stderrWriter

type stderrWriter struct{}

func (stderrWriter) Write(p []byte) (int, error) {
	return writeStderr(p)
}
