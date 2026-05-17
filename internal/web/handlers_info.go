package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/optimumsage/superkube/internal/audit"
	"github.com/optimumsage/superkube/internal/config"
	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/kubectl"
	"github.com/optimumsage/superkube/internal/version"
)

// apiInfo returns global state the SPA shell needs on every navigation:
// versions, the active context/namespace banner, and the CSRF token for HTMX
// to echo back. The shell calls this once on load and re-polls after a ctx/ns
// switch so the banner updates.
func (s *Server) apiInfo(w http.ResponseWriter, r *http.Request) {
	sess := s.readSession(r)
	loader := s.loader(sess)

	currentCtx := sess.Context
	if currentCtx == "" {
		currentCtx, _ = loader.CurrentContext()
	}
	currentNS := sess.Namespace
	if currentNS == "" {
		currentNS, _ = loader.CurrentNamespace()
	}
	// Dep callbacks are optional — tests may pass a partial Deps struct.
	bannerText, bannerKind := "", ""
	if s.deps.Banner != nil {
		bannerText, bannerKind = s.deps.Banner()
	}

	kubeVer := ""
	if s.deps.Runner != nil {
		if v, err := kubectlVersion(r.Context(), s.deps.Runner); err == nil {
			kubeVer = v
		}
	}
	aiName := ""
	if s.deps.AIProvider != nil {
		if p, err := s.deps.AIProvider(); err == nil && p != nil {
			aiName = p.Name()
		}
	}

	resp := map[string]any{
		"version":   version.String(),
		"kubectl":   kubeVer,
		"ai":        aiName,
		"context":   currentCtx,
		"namespace": currentNS,
		"banner": map[string]string{
			"text": bannerText,
			"kind": bannerKind,
		},
		"csrf":  s.csrf.ensureCookie(w, r),
		"token": tokenIfRequired(s.cfg.Token),
	}
	s.render.JSON(w, http.StatusOK, resp)
}

// tokenIfRequired echoes back the URL token so the shell can attach it to
// SSE / WebSocket URLs (which can't carry the cookie reliably across all
// browsers). Empty when no token is required.
func tokenIfRequired(t string) string {
	return t
}

// kubectlVersion returns a short kubectl version string. We use a short
// timeout because asking kubectl for its version goes through the apiserver
// in newer releases and can hang if the cluster is unreachable.
func kubectlVersion(ctx context.Context, runner *kubectl.Runner) (string, error) {
	if runner == nil {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	v, err := runner.Version(ctx)
	if err != nil {
		return "", err
	}
	return v.GitVersion, nil
}

// apiContextsList returns every context in the merged kubeconfig.
func (s *Server) apiContextsList(w http.ResponseWriter, r *http.Request) {
	sess := s.readSession(r)
	names, err := s.loader(sess).ListContexts()
	if err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	current, _ := s.loader(sess).CurrentContext()
	s.render.JSON(w, http.StatusOK, map[string]any{
		"current":  current,
		"selected": sess.Context,
		"items":    names,
	})
}

// apiContextSwitch updates the user's *session* selection (cookie). To make
// the change permanent, the UI exposes a "Make default" button that hits
// /api/v1/contexts/switch?persist=true.
func (s *Server) apiContextSwitch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Context string `json:"context"`
		Persist bool   `json:"persist"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.render.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Context == "" {
		s.render.Error(w, http.StatusBadRequest, "context required")
		return
	}
	sess := s.readSession(r)
	loader := s.loader(sess)

	names, err := loader.ListContexts()
	if err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !contains(names, body.Context) {
		s.render.Error(w, http.StatusNotFound, "context not found")
		return
	}

	if body.Persist {
		if err := loader.SwitchContext(body.Context, config.StateDir()); err != nil {
			s.render.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// Cookie persistence: scoped to this server's session. ns cookie is
	// cleared because a new context may have its own pinned namespace.
	http.SetCookie(w, &http.Cookie{
		Name: sessCtxCookie, Value: body.Context,
		Path: "/", SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: sessNsCookie, Value: "",
		Path: "/", MaxAge: -1, SameSite: http.SameSiteStrictMode,
	})

	s.recordWebAudit("ctx", []string{body.Context}, 0, time.Since(time.Now()))
	s.render.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiNamespacesList returns the namespaces visible in the current cluster.
// We prefer the live cluster list (most useful in practice); fall back to the
// kubeconfig-pinned namespaces if the cluster is unreachable.
func (s *Server) apiNamespacesList(w http.ResponseWriter, r *http.Request) {
	sess := s.readSession(r)
	loader := s.loader(sess)

	// Best-effort cluster query (short timeout so a slow/unreachable cluster
	// doesn't hang the UI).
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	names, err := listClusterNamespaces(ctx, loader, sess.Context)
	if err != nil || len(names) == 0 {
		names, _ = loader.ListNamespaces()
	}
	current, _ := loader.CurrentNamespace()
	s.render.JSON(w, http.StatusOK, map[string]any{
		"current":  current,
		"selected": sess.Namespace,
		"items":    names,
	})
}

// listClusterNamespaces is a thin client-go GET that returns sorted namespace
// names. Returns an error when the cluster is unreachable so the caller can
// fall back to the kubeconfig-pinned list.
func listClusterNamespaces(ctx context.Context, loader kube.Loader, _ string) ([]string, error) {
	cs, err := loader.Clientset()
	if err != nil {
		return nil, err
	}
	nsList, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		names = append(names, ns.Name)
	}
	sort.Strings(names)
	return names, nil
}

// apiNamespaceSwitch sets the cookie namespace selection. Optional persist
// rewrites kubeconfig (same semantics as apiContextSwitch).
func (s *Server) apiNamespaceSwitch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Namespace string `json:"namespace"`
		Persist   bool   `json:"persist"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.render.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Persist {
		sess := s.readSession(r)
		if err := s.loader(sess).SwitchNamespace(body.Namespace, config.StateDir()); err != nil {
			s.render.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessNsCookie, Value: body.Namespace,
		Path: "/", SameSite: http.SameSiteStrictMode,
	})
	s.render.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// recordWebAudit writes one audit entry for a web-initiated verb. Wrapper so
// individual handlers don't repeat the boilerplate.
func (s *Server) recordWebAudit(verb string, argv []string, exitCode int, dur time.Duration) {
	if s.deps.Audit == nil {
		return
	}
	cmd := "sk web " + verb
	full := append([]string{"sk", "web", verb}, argv...)
	s.deps.Audit(audit.Event{
		User:       webUser(),
		Cmd:        cmd,
		Argv:       full,
		Context:    s.deps.Context,
		Namespace:  s.deps.Namespace,
		Kubeconfig: s.deps.Kubeconfig,
		Verb:       verb,
		DurationMS: dur.Milliseconds(),
		ExitCode:   exitCode,
	})
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
