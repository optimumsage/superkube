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
	// Pin the effective context. Policy, banner, audit and every client-go /
	// kubectl call must agree on which cluster this request targets, so when
	// neither the cookie nor `sk --context` named one we resolve the
	// kubeconfig's current-context here rather than letting each consumer
	// guess independently.
	if sess.Context == "" {
		if c, err := s.kubeconfigLoader(sess).CurrentContext(); err == nil {
			sess.Context = c
		}
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
