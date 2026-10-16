package web

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/optimumsage/superkube/internal/kube"
)

// session is the per-request view of "what context + namespace is the user
// looking at right now?". The web UI lets the user switch context/namespace
// via the header dropdowns; their choice is persisted in cookies so it
// survives a page reload without rewriting their kubeconfig (we only update
// kubeconfig on an explicit "Make default" action).
type session struct {
	Context    string
	Namespace  string
	Kubeconfig string
}

const (
	sessCtxCookie = "sk_ctx"
	sessNsCookie  = "sk_ns"
)

type sessionKey struct{}

// lazySession resolves the snapshot on first use and caches it for the rest of
// the request.
type lazySession struct {
	once sync.Once
	val  session
}

// withSession arranges for an immutable session snapshot to be pinned on the
// request on first readSession. Every later call in the same request returns
// that snapshot, so the context and namespace that were authorized, digested
// and audited are exactly the ones passed to kubectl/helm even if
// kubeconfig's current-context changes mid-request. Resolution is lazy (it
// parses kubeconfig), so static assets and handlers that never read the
// session pay nothing.
func (s *Server) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/favicon.ico" {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, &lazySession{})))
	})
}

// readSession returns the request's pinned snapshot (resolving it once, on
// first call).
func (s *Server) readSession(r *http.Request) session {
	if ls, ok := r.Context().Value(sessionKey{}).(*lazySession); ok {
		ls.once.Do(func() { ls.val = s.resolveSession(r) })
		return ls.val
	}
	return s.resolveSession(r)
}

// resolveSession derives the concrete context + effective namespace:
// context: cookie -> `sk --context` -> kubeconfig current-context;
// namespace: cookie -> `sk -n` -> that context's kubeconfig namespace -> "default".
func (s *Server) resolveSession(r *http.Request) session {
	sess := session{
		Context:    s.deps.Context,
		Namespace:  s.deps.Namespace,
		Kubeconfig: s.deps.Kubeconfig,
	}
	if c, err := r.Cookie(sessCtxCookie); err == nil && c.Value != "" && !badToken(c.Value) {
		sess.Context = c.Value
	}
	if c, err := r.Cookie(sessNsCookie); err == nil && c.Value != "" && !badToken(c.Value) {
		sess.Namespace = c.Value
	}
	l := s.deps.Loader
	l.KubeconfigPath = sess.Kubeconfig
	l.Context = sess.Context
	if ctxName, ns, err := l.CurrentContextAndNamespace(); err == nil {
		sess.Context = ctxName
		if sess.Namespace == "" {
			sess.Namespace = ns
		}
	}
	if sess.Namespace == "" {
		sess.Namespace = "default"
	}
	return sess
}

// loader returns a kube.Loader configured for this session's kubeconfig.
// Always re-derived (the struct is cheap) so a context switch in the UI
// applies immediately on the next request.
func (s *Server) loader(sess session) kube.Loader {
	l := s.deps.Loader
	l.KubeconfigPath = sess.Kubeconfig
	// The loader copy must follow the UI's context switch, otherwise client-go
	// views stay on the startup context while kubectl calls (which get
	// --context from the session) hit a different cluster.
	l.Context = sess.Context
	return l
}

// kubeconfigLoader is a loader that ignores the session's context override.
// It reports the kubeconfig's own current-context.
func (s *Server) kubeconfigLoader(sess session) kube.Loader {
	l := s.deps.Loader
	l.KubeconfigPath = sess.Kubeconfig
	l.Context = ""
	return l
}

// kubectlArgs prefixes args with the session's global flags, skipping the -n
// injection when args already carry an explicit namespace.
func (sess session) kubectlArgs(args []string) []string {
	if hasFlagN(args) {
		return sess.prependGlobalFlagsNoNS(args)
	}
	return sess.prependGlobalFlags(args)
}

// prependGlobalFlags returns args prefixed with --kubeconfig/--context/-n
// when those are set in the session. Mirrors kubectl.PrependGlobalFlags but
// for session state (the latter reads from cli.Flags which is process-wide).
func (sess session) prependGlobalFlags(args []string) []string {
	out := make([]string, 0, len(args)+6)
	if sess.Kubeconfig != "" {
		out = append(out, "--kubeconfig", sess.Kubeconfig)
	}
	if sess.Context != "" {
		out = append(out, "--context", sess.Context)
	}
	if sess.Namespace != "" {
		out = append(out, "-n", sess.Namespace)
	}
	return append(out, args...)
}

// prependGlobalFlagsNoNS is the same as prependGlobalFlags but skips the -n
// injection. Callers use it when args already carries a namespace flag.
func (sess session) prependGlobalFlagsNoNS(args []string) []string {
	out := make([]string, 0, len(args)+4)
	if sess.Kubeconfig != "" {
		out = append(out, "--kubeconfig", sess.Kubeconfig)
	}
	if sess.Context != "" {
		out = append(out, "--context", sess.Context)
	}
	return append(out, args...)
}
