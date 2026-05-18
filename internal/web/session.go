package web

import (
	"net/http"

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

// readSession derives the active session from cookies, falling back to the
// server's deps (which were initialized from `sk web`'s root flags + the
// user's current kubeconfig).
func (s *Server) readSession(r *http.Request) session {
	sess := session{
		Context:    s.deps.Context,
		Namespace:  s.deps.Namespace,
		Kubeconfig: s.deps.Kubeconfig,
	}
	if c, err := r.Cookie(sessCtxCookie); err == nil && c.Value != "" {
		sess.Context = c.Value
	}
	if c, err := r.Cookie(sessNsCookie); err == nil && c.Value != "" {
		sess.Namespace = c.Value
	}
	return sess
}

// loader returns a kube.Loader configured for this session's kubeconfig.
// Always re-derived (the struct is cheap) so a context switch in the UI
// applies immediately on the next request.
func (s *Server) loader(sess session) kube.Loader {
	l := s.deps.Loader
	l.KubeconfigPath = sess.Kubeconfig
	return l
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
