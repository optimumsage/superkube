package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/optimumsage/superkube/internal/helm"
)

// handlers_helm.go owns every /api/v1/helm/* endpoint. Helm operations shell
// out to the user's `helm` binary; when the binary is missing the server
// returns 503 + JSON {installed:false} on every endpoint except /status, so
// the SPA can render the "helm not installed" UX instead of a generic error.
//
// Mutations (rollback / uninstall / upgrade / install / repo remove) follow
// the same preview→confirm-token flow as the kubectl destructive handlers in
// handlers_destructive.go.

// helmRequired short-circuits with 503 when helm is not installed. Returns
// true if it wrote a response (caller must return early).
func (s *Server) helmRequired(w http.ResponseWriter) bool {
	if s.deps.Helm != nil {
		return false
	}
	s.render.JSON(w, http.StatusServiceUnavailable, map[string]any{
		"installed": false,
		"error":     "helm binary not installed",
	})
	return true
}

// helmSessionArgs prepends --kube-context and --kubeconfig from the session
// onto a helm argv. Namespace is NOT injected — helm callers carry the -n
// they need on a per-release basis.
func (s *Server) helmSessionArgs(r *http.Request, args []string) []string {
	sess := s.readSession(r)
	out := make([]string, 0, len(args)+4)
	if sess.Kubeconfig != "" {
		out = append(out, "--kubeconfig", sess.Kubeconfig)
	}
	if sess.Context != "" {
		out = append(out, "--kube-context", sess.Context)
	}
	return append(out, args...)
}

// runHelmCaptured runs helm with the given args plus session globals, and
// returns combined output + exit code.
func (s *Server) runHelmCaptured(r *http.Request, args []string) (string, int) {
	full := s.helmSessionArgs(r, args)
	var out bytes.Buffer
	err := s.deps.Helm.Run(r.Context(), full, helm.RunOpts{Stdout: &out, Stderr: &out})
	if err != nil {
		return out.String(), helmExitCodeOf(err)
	}
	return out.String(), 0
}

func helmExitCodeOf(err error) int {
	var ee *helm.ExitCodeError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return 1
}

// --- status ----------------------------------------------------------------

// apiHelmStatus is the only endpoint that works without helm — it tells the
// UI whether to render the rest of the Helm section.
func (s *Server) apiHelmStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"installed":         false,
		"version":           "",
		"releases_detected": 0,
	}
	if s.deps.Helm != nil {
		resp["installed"] = true
		if v, err := s.deps.Helm.Version(r.Context()); err == nil {
			resp["version"] = v
		}
	}
	// Detection works without the binary — it scans Kubernetes secrets. Cap
	// to 2s so a stale kubeconfig pointing at an unreachable cluster doesn't
	// hang the dashboard load.
	sess := s.readSession(r)
	probeCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	refs, err := helm.DetectReleases(probeCtx, s.loader(sess), "")
	if err == nil {
		resp["releases_detected"] = len(refs)
		resp["releases"] = refs
	}
	s.render.JSON(w, http.StatusOK, resp)
}

// --- list / detail ---------------------------------------------------------

func (s *Server) apiHelmReleasesList(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	args := []string{"list", "-o", "json"}
	if r.URL.Query().Get("all") == "1" {
		args = append(args, "-A")
	} else if ns := s.readSession(r).Namespace; ns != "" {
		args = append(args, "-n", ns)
	}
	out, code := s.runHelmCaptured(r, args)
	if code != 0 {
		s.render.JSON(w, statusForExit(code), map[string]any{"error": out, "exit_code": code})
		return
	}
	var items []helm.Release
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		s.render.JSON(w, http.StatusOK, map[string]any{"items": []helm.Release{}, "raw": out})
		return
	}
	s.render.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) apiHelmReleaseStatus(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	args := []string{"status", name, "-n", ns, "-o", "json"}
	if rev := r.URL.Query().Get("revision"); rev != "" {
		args = append(args, "--revision", rev)
	}
	if r.URL.Query().Get("show-resources") == "1" {
		args = append(args, "--show-resources")
	}
	out, code := s.runHelmCaptured(r, args)
	if code != 0 {
		s.render.JSON(w, statusForExit(code), map[string]any{"error": out, "exit_code": code})
		return
	}
	// Pass-through JSON body so the UI can render any field helm emitted.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(out))
}

func (s *Server) apiHelmReleaseValues(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	args := []string{"get", "values", name, "-n", ns, "-o", "yaml"}
	if r.URL.Query().Get("computed") == "1" {
		args = append(args, "-a")
	}
	if rev := r.URL.Query().Get("revision"); rev != "" {
		args = append(args, "--revision", rev)
	}
	out, code := s.runHelmCaptured(r, args)
	s.writeTextResult(w, out, code)
}

func (s *Server) apiHelmReleaseManifest(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	args := []string{"get", "manifest", name, "-n", ns}
	if rev := r.URL.Query().Get("revision"); rev != "" {
		args = append(args, "--revision", rev)
	}
	out, code := s.runHelmCaptured(r, args)
	s.writeTextResult(w, out, code)
}

func (s *Server) apiHelmReleaseNotes(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	args := []string{"get", "notes", name, "-n", ns}
	if rev := r.URL.Query().Get("revision"); rev != "" {
		args = append(args, "--revision", rev)
	}
	out, code := s.runHelmCaptured(r, args)
	s.writeTextResult(w, out, code)
}

func (s *Server) apiHelmReleaseHooks(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	args := []string{"get", "hooks", name, "-n", ns}
	if rev := r.URL.Query().Get("revision"); rev != "" {
		args = append(args, "--revision", rev)
	}
	out, code := s.runHelmCaptured(r, args)
	s.writeTextResult(w, out, code)
}

func (s *Server) apiHelmReleaseHistory(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	args := []string{"history", name, "-n", ns, "-o", "json"}
	out, code := s.runHelmCaptured(r, args)
	if code != 0 {
		s.render.JSON(w, statusForExit(code), map[string]any{"error": out, "exit_code": code})
		return
	}
	var items []helm.Revision
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		s.render.JSON(w, http.StatusOK, map[string]any{"items": []helm.Revision{}, "raw": out})
		return
	}
	s.render.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// --- mutations -------------------------------------------------------------

// --- search & chart values -------------------------------------------------

func (s *Server) apiHelmSearch(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	term := r.URL.Query().Get("term")
	args := []string{"search", "repo", "-o", "json"}
	if r.URL.Query().Get("versions") == "1" {
		args = append(args, "--versions")
	}
	if term != "" {
		args = append(args, term)
	} else {
		// `helm search repo` requires at least one arg; "" lists everything.
		args = append(args, "")
	}
	out, code := s.runHelmCaptured(r, args)
	if code != 0 {
		s.render.JSON(w, statusForExit(code), map[string]any{"error": out, "exit_code": code})
		return
	}
	var items []helm.SearchHit
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		s.render.JSON(w, http.StatusOK, map[string]any{"items": []helm.SearchHit{}, "raw": out})
		return
	}
	s.render.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// apiHelmChartValues returns the default values YAML for repo/chart. Path is
// flat — we accept ?repo=...&chart=... so chart names with slashes are okay.
func (s *Server) apiHelmChartValues(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	repo := r.URL.Query().Get("repo")
	chart := r.URL.Query().Get("chart")
	if repo == "" || chart == "" {
		s.render.Error(w, http.StatusBadRequest, "repo and chart required")
		return
	}
	args := []string{"show", "values", repo + "/" + chart}
	if v := r.URL.Query().Get("version"); v != "" {
		args = append(args, "--version", v)
	}
	out, code := s.runHelmCaptured(r, args)
	s.writeTextResult(w, out, code)
}

// --- helpers ---------------------------------------------------------------

// writeTextResult writes helm output as plain text on success and JSON on
// error (so the SPA can distinguish).
func (s *Server) writeTextResult(w http.ResponseWriter, out string, code int) {
	if code != 0 {
		s.render.JSON(w, statusForExit(code), map[string]any{"error": out, "exit_code": code})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(out))
}

// redactHelmPassword replaces the value after --password (and --username, for
// good measure) with "REDACTED" before sending to audit.
func redactHelmPassword(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i < len(out)-1; i++ {
		if out[i] == "--password" || out[i] == "--username" {
			out[i+1] = "REDACTED"
		}
	}
	return out
}
