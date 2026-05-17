// Package web hosts the local-only HTTP server exposed by `sk web`. It is a
// thin adapter on top of the same kube / guardrail / ai / audit / portforward
// packages the CLI uses, so a destructive web action takes the same guarded
// path as the equivalent CLI command (policy check → typed confirmation →
// kubectl shell-out → audit). The package does not contain its own kube
// client; everything flows through Deps that the cli layer wires up.
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/optimumsage/superkube/internal/ai"
	"github.com/optimumsage/superkube/internal/audit"
	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/kubectl"
)

// Deps is the set of collaborators the cli layer hands to the server. Keeping
// these as interfaces/functions instead of direct imports of internal/cli
// avoids a circular dependency.
type Deps struct {
	Loader     kube.Loader
	Runner     *kubectl.Runner
	Policy     func() guardrail.Policy
	Banner     func() (text, kind string) // mirrors cli.ActiveBanner
	Audit      func(audit.Event)
	AIProvider func() (ai.Provider, error) // returns the resolved provider for this invocation

	// Flag mirrors — read-only snapshots of cli.Flags so the web layer doesn't
	// import the cli package. Updates here don't reach the cli flags.
	Context    string
	Namespace  string
	Kubeconfig string
	NoContext  bool
	Yes        bool
}

// Config describes how the server binds. Constructed from `sk web` flags.
type Config struct {
	Bind   string // e.g. "127.0.0.1"
	Port   int    // 0 = pick a free port
	Token  string // required when Bind is non-loopback; auto-generated if empty
	NoOpen bool   // skip auto-opening the browser
}

// Server is the running HTTP server.
type Server struct {
	cfg    Config
	deps   Deps
	mux    *http.ServeMux
	tmpls  *template.Template
	srv    *http.Server
	addr   string // resolved listen address (host:port)
	url    string // public URL incl. token if any
	csrf   *csrfStore
	pty    *ptyConfirmStore
	render renderFns
}

// New builds a Server. It parses the embedded templates and registers all
// routes; call Run to start listening.
func New(cfg Config, deps Deps) (*Server, error) {
	if cfg.Bind == "" {
		cfg.Bind = "127.0.0.1"
	}
	if cfg.Token == "" && !isLoopback(cfg.Bind) {
		cfg.Token = randomToken(32)
	}

	tmpls, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	s := &Server{
		cfg:   cfg,
		deps:  deps,
		mux:   http.NewServeMux(),
		tmpls: tmpls,
		csrf:  newCSRFStore(),
		pty:   newPtyConfirmStore(30 * time.Second),
	}
	s.render = newRenderFns(tmpls)
	s.registerRoutes()
	return s, nil
}

// Run binds the listener, optionally opens the browser, and serves until ctx
// is cancelled. Returns the cause if Serve fails for any non-graceful reason.
func (s *Server) Run(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Bind, s.cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	s.addr = ln.Addr().String()
	host := s.cfg.Bind
	if isLoopback(host) {
		host = "127.0.0.1"
	}
	port := s.addr[strings.LastIndex(s.addr, ":")+1:]
	s.url = fmt.Sprintf("http://%s:%s", host, port)
	if s.cfg.Token != "" {
		s.url += "/?token=" + s.cfg.Token
	}

	handler := s.middleware(s.mux)
	s.srv = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Fprintf(os.Stderr, "superkube web listening at %s\n", s.url)
	if !s.cfg.NoOpen {
		if err := openBrowser(s.url); err != nil {
			fmt.Fprintf(os.Stderr, "(could not auto-open browser: %v)\n", err)
		}
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Addr returns the resolved listen address ("host:port"). Only valid after Run
// has started listening; useful for tests.
func (s *Server) Addr() string { return s.addr }

// URL returns the canonical URL to open in a browser.
func (s *Server) URL() string { return s.url }

// allowedHosts reports the set of acceptable Host header values. We use this
// for DNS-rebind protection: even though the listener is bound to 127.0.0.1
// by default, a malicious page can resolve a name like sk.attacker.com to
// 127.0.0.1 and pivot through the browser. Restricting Host stops that.
func (s *Server) allowedHosts() []string {
	hosts := []string{"127.0.0.1", "localhost", "[::1]", "::1"}
	if !isLoopback(s.cfg.Bind) {
		hosts = append(hosts, s.cfg.Bind)
	}
	return hosts
}

// isLoopback reports whether bind is one of the loopback identifiers we trust
// to need no token by default.
func isLoopback(bind string) bool {
	switch bind {
	case "127.0.0.1", "localhost", "::1", "[::1]", "":
		return true
	}
	return false
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Extremely unlikely; fall back to time-based pseudo-random so we
		// never ship an empty token.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// readEmbedFS returns the embedded filesystem rooted at "static". Exported via
// a helper so handlers and tests can serve assets without re-walking.
func readEmbedFS() (fs.FS, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	return sub, nil
}

// snap captures a small per-request snapshot of mutable Deps state. Handlers
// pass this around instead of touching s.deps directly, which keeps tests
// honest (a test server can drive a snapshot without poking package globals).
type snap struct {
	Deps
}

func (s *Server) snapshot() snap { return snap{Deps: s.deps} }

// once-only template parse guard so tests that build many servers don't
// re-parse the same FS repeatedly.
var (
	tmplOnce sync.Once
	tmplVal  *template.Template
	tmplErr  error
)

func parseTemplates() (*template.Template, error) {
	tmplOnce.Do(func() {
		sub, err := fs.Sub(staticFS, "static/templates")
		if err != nil {
			tmplErr = err
			return
		}
		funcs := template.FuncMap{
			"upper": strings.ToUpper,
			"lower": strings.ToLower,
			"sub": func(a, b int) int { return a - b },
		}
		t, err := template.New("").Funcs(funcs).ParseFS(sub, "*.html")
		if err != nil {
			tmplErr = err
			return
		}
		tmplVal = t
	})
	return tmplVal, tmplErr
}
