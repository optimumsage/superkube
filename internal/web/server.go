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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/optimumsage/superkube/internal/ai"
	"github.com/optimumsage/superkube/internal/audit"
	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/helm"
	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/kubectl"
)

// Deps is the set of collaborators the cli layer hands to the server. Keeping
// these as interfaces/functions instead of direct imports of internal/cli
// avoids a circular dependency.
type Deps struct {
	Loader kube.Loader
	Runner *kubectl.Runner
	Helm   *helm.Runner // nil when the helm binary is not installed; handlers degrade gracefully
	// PolicyFor resolves the effective guardrail policy for a kube context.
	// It is called per request with the *session's* context (cookie-selected,
	// which can differ from the one `sk web` started in). An error means the
	// config could not be evaluated and destructive actions must be refused.
	// nil means "no policy" (tests / embedders).
	PolicyFor func(kubeContext string) (guardrail.Policy, error)
	// BannerFor returns the prod-context banner for a kube context.
	BannerFor  func(kubeContext string) (text, kind string)
	Audit      func(audit.Event)
	AIProvider func() (ai.Provider, error) // returns the resolved provider for this invocation

	// Flag mirrors — read-only snapshots of cli.Flags so the web layer doesn't
	// import the cli package. Updates here don't reach the cli flags.
	Context    string
	Namespace  string
	Kubeconfig string
	NoContext  bool
	// Yes mirrors `sk web --yes`: the server operator (not the browser) opted
	// out of confirmation prompts. It never overrides policy `forbid` rules
	// nor the typed DELETE phrase for `delete --all`.
	Yes bool
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
	hosts  []string // acceptable Host header values (DNS-rebind defense)
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
	hostname, _ := os.Hostname()
	addrs, _ := net.InterfaceAddrs()
	s.hosts = computeAllowedHosts(cfg.Bind, addrs, hostname)
	s.registerRoutes()
	return s, nil
}

// Run binds the listener, optionally opens the browser, and serves until ctx
// is cancelled. Returns the cause if Serve fails for any non-graceful reason.
func (s *Server) Run(ctx context.Context) error {
	addr := net.JoinHostPort(strings.Trim(s.cfg.Bind, "[]"), strconv.Itoa(s.cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	s.addr = ln.Addr().String()
	port := s.addr[strings.LastIndex(s.addr, ":")+1:]
	base := "http://" + net.JoinHostPort(s.publicHost(), port)
	s.url = base
	if s.cfg.Token != "" {
		s.url += "/?token=" + s.cfg.Token
	}

	s.srv = newHTTPServer(s.middleware(s.mux))

	// Service managers capture stderr into a log file; never write the
	// credentialed URL there. On a terminal the operator needs it to log in.
	if s.cfg.Token != "" && !stderrIsTTY() {
		_, _ = fmt.Fprintf(startupOut, "superkube web listening at %s (auth token required; it was not printed because stderr is not a terminal)\n", base)
	} else {
		_, _ = fmt.Fprintf(startupOut, "superkube web listening at %s\n", s.url)
	}
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
			"sub":   func(a, b int) int { return a - b },
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
