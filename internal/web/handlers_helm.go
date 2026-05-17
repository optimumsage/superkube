package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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

type helmRollbackRequest struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Revision     int    `json:"revision"`
	Wait         bool   `json:"wait"`
	Force        bool   `json:"force"`
	Yes          bool   `json:"yes"`
	ConfirmToken string `json:"confirm_token"`
}

func (s *Server) apiHelmRollback(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	var body helmRollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.Namespace == "" || body.Revision <= 0 {
		s.render.Error(w, http.StatusBadRequest, "name, namespace, revision required")
		return
	}
	args := []string{"rollback", body.Name, strconv.Itoa(body.Revision), "-n", body.Namespace}
	if body.Wait {
		args = append(args, "--wait")
	}
	if body.Force {
		args = append(args, "--force")
	}
	if s.gateForbidden(w, "helm", args) {
		return
	}
	if body.ConfirmToken == "" && !body.Yes {
		t := s.pty.Issue(ptyConfirmEntry{Verb: "helm.rollback", Resource: body.Namespace + "/" + body.Name, Argv: args})
		s.render.JSON(w, http.StatusOK, confirmationResponse{
			Status: "needs_confirmation", Style: "yes_no",
			Prompt: fmt.Sprintf("Roll back %s to revision %d?", body.Name, body.Revision),
			Detail: "Helm will re-apply the chart manifests from the chosen revision.",
			Token:  t, TTLSeconds: 30,
		})
		return
	}
	if body.ConfirmToken != "" {
		if _, ok := s.pty.Consume(body.ConfirmToken, ""); !ok {
			s.render.Error(w, http.StatusGone, "confirmation expired")
			return
		}
	}
	start := time.Now()
	out, code := s.runHelmCaptured(r, args)
	s.recordWebAudit("helm", args, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": out, "exit_code": code})
}

type helmUninstallRequest struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	KeepHistory  bool   `json:"keep_history"`
	Yes          bool   `json:"yes"`
	ConfirmToken string `json:"confirm_token"`
	ConfirmValue string `json:"confirm_value"`
}

func (s *Server) apiHelmUninstall(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	var body helmUninstallRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.Namespace == "" {
		s.render.Error(w, http.StatusBadRequest, "name, namespace required")
		return
	}
	args := []string{"uninstall", body.Name, "-n", body.Namespace}
	if body.KeepHistory {
		args = append(args, "--keep-history")
	}
	if s.gateForbidden(w, "helm", args) {
		return
	}
	if body.ConfirmToken == "" && !body.Yes {
		t := s.pty.Issue(ptyConfirmEntry{Verb: "helm.uninstall", Resource: body.Namespace + "/" + body.Name, Expect: body.Name, Argv: args})
		s.render.JSON(w, http.StatusOK, confirmationResponse{
			Status: "needs_confirmation", Style: "typed_name",
			Prompt: "Type the release name to uninstall",
			Detail: "This will remove every resource managed by the release.",
			Expect: body.Name, Token: t, TTLSeconds: 30,
		})
		return
	}
	if body.ConfirmToken != "" {
		if _, ok := s.pty.Consume(body.ConfirmToken, body.ConfirmValue); !ok {
			s.render.Error(w, http.StatusGone, "confirmation did not match")
			return
		}
	}
	start := time.Now()
	out, code := s.runHelmCaptured(r, args)
	s.recordWebAudit("helm", args, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": out, "exit_code": code})
}

type helmUpgradeRequest struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	Chart           string `json:"chart"`
	Version         string `json:"version"`
	Values          string `json:"values"` // YAML values document
	ReuseValues     bool   `json:"reuse_values"`
	ResetValues     bool   `json:"reset_values"`
	Install         bool   `json:"install"` // --install (upgrade or install)
	CreateNamespace bool   `json:"create_namespace"`
	Wait            bool   `json:"wait"`
	Atomic          bool   `json:"atomic"`
	Yes             bool   `json:"yes"`
	ConfirmToken    string `json:"confirm_token"`
}

// apiHelmUpgradePreview runs `helm upgrade --dry-run` and returns the rendered
// manifest so the SPA can show it as a preview before committing.
func (s *Server) apiHelmUpgradePreview(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	var body helmUpgradeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.Namespace == "" || body.Chart == "" {
		s.render.Error(w, http.StatusBadRequest, "name, namespace, chart required")
		return
	}
	args, valuesPath, cleanup, err := buildUpgradeArgs(body, true)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		s.render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = valuesPath
	out, code := s.runHelmCaptured(r, args)
	if code != 0 {
		s.render.JSON(w, statusForExit(code), map[string]any{"error": out, "exit_code": code})
		return
	}
	t := s.pty.Issue(ptyConfirmEntry{Verb: "helm.upgrade", Resource: body.Namespace + "/" + body.Name, Argv: args})
	s.render.JSON(w, http.StatusOK, map[string]any{
		"status":        "needs_confirmation",
		"rendered":      out,
		"confirm_token": t,
		"ttl":           30,
	})
}

func (s *Server) apiHelmUpgradeCommit(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	var body helmUpgradeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.Namespace == "" || body.Chart == "" {
		s.render.Error(w, http.StatusBadRequest, "name, namespace, chart required")
		return
	}
	if body.ConfirmToken == "" && !body.Yes {
		s.render.Error(w, http.StatusBadRequest, "confirm_token required (or set yes=true)")
		return
	}
	if body.ConfirmToken != "" {
		if _, ok := s.pty.Consume(body.ConfirmToken, ""); !ok {
			s.render.Error(w, http.StatusGone, "confirmation expired")
			return
		}
	}
	args, _, cleanup, err := buildUpgradeArgs(body, false)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		s.render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.gateForbidden(w, "helm", args) {
		return
	}
	start := time.Now()
	out, code := s.runHelmCaptured(r, args)
	s.recordWebAudit("helm", args, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": out, "exit_code": code})
}

// buildUpgradeArgs constructs the helm upgrade argv, writing values YAML to a
// temp file when provided. Caller must call cleanup() (may be nil).
func buildUpgradeArgs(body helmUpgradeRequest, dryRun bool) (args []string, valuesPath string, cleanup func(), err error) {
	args = []string{"upgrade", body.Name, body.Chart, "-n", body.Namespace}
	if body.Install {
		args = append(args, "--install")
	}
	if body.CreateNamespace {
		args = append(args, "--create-namespace")
	}
	if body.Version != "" {
		args = append(args, "--version", body.Version)
	}
	if body.ReuseValues {
		args = append(args, "--reuse-values")
	}
	if body.ResetValues {
		args = append(args, "--reset-values")
	}
	if body.Wait {
		args = append(args, "--wait")
	}
	if body.Atomic {
		args = append(args, "--atomic")
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if body.Values != "" {
		valuesPath, cleanup, err = writeTempYAML(body.Values)
		if err != nil {
			return nil, "", cleanup, err
		}
		args = append(args, "-f", valuesPath)
	}
	return args, valuesPath, cleanup, nil
}

type helmInstallRequest struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	Chart           string `json:"chart"`
	Version         string `json:"version"`
	Values          string `json:"values"`
	CreateNamespace bool   `json:"create_namespace"`
	Wait            bool   `json:"wait"`
	Atomic          bool   `json:"atomic"`
	Yes             bool   `json:"yes"`
	ConfirmToken    string `json:"confirm_token"`
}

func (s *Server) apiHelmInstallPreview(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	var body helmInstallRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.Namespace == "" || body.Chart == "" {
		s.render.Error(w, http.StatusBadRequest, "name, namespace, chart required")
		return
	}
	args, _, cleanup, err := buildInstallArgs(body, true)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		s.render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	out, code := s.runHelmCaptured(r, args)
	if code != 0 {
		s.render.JSON(w, statusForExit(code), map[string]any{"error": out, "exit_code": code})
		return
	}
	t := s.pty.Issue(ptyConfirmEntry{Verb: "helm.install", Resource: body.Namespace + "/" + body.Name, Argv: args})
	s.render.JSON(w, http.StatusOK, map[string]any{
		"status":        "needs_confirmation",
		"rendered":      out,
		"confirm_token": t,
		"ttl":           30,
	})
}

func (s *Server) apiHelmInstallCommit(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	var body helmInstallRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.Namespace == "" || body.Chart == "" {
		s.render.Error(w, http.StatusBadRequest, "name, namespace, chart required")
		return
	}
	if body.ConfirmToken == "" && !body.Yes {
		s.render.Error(w, http.StatusBadRequest, "confirm_token required (or set yes=true)")
		return
	}
	if body.ConfirmToken != "" {
		if _, ok := s.pty.Consume(body.ConfirmToken, ""); !ok {
			s.render.Error(w, http.StatusGone, "confirmation expired")
			return
		}
	}
	args, _, cleanup, err := buildInstallArgs(body, false)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		s.render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.gateForbidden(w, "helm", args) {
		return
	}
	start := time.Now()
	out, code := s.runHelmCaptured(r, args)
	s.recordWebAudit("helm", args, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": out, "exit_code": code})
}

func buildInstallArgs(body helmInstallRequest, dryRun bool) (args []string, valuesPath string, cleanup func(), err error) {
	args = []string{"install", body.Name, body.Chart, "-n", body.Namespace}
	if body.CreateNamespace {
		args = append(args, "--create-namespace")
	}
	if body.Version != "" {
		args = append(args, "--version", body.Version)
	}
	if body.Wait {
		args = append(args, "--wait")
	}
	if body.Atomic {
		args = append(args, "--atomic")
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if body.Values != "" {
		valuesPath, cleanup, err = writeTempYAML(body.Values)
		if err != nil {
			return nil, "", cleanup, err
		}
		args = append(args, "-f", valuesPath)
	}
	return args, valuesPath, cleanup, nil
}

// --- repos -----------------------------------------------------------------

func (s *Server) apiHelmReposList(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	args := []string{"repo", "list", "-o", "json"}
	out, code := s.runHelmCaptured(r, args)
	if code != 0 {
		// helm exits non-zero when zero repos are configured — return empty list.
		s.render.JSON(w, http.StatusOK, map[string]any{"items": []helm.Repo{}})
		return
	}
	var items []helm.Repo
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		s.render.JSON(w, http.StatusOK, map[string]any{"items": []helm.Repo{}, "raw": out})
		return
	}
	s.render.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type helmRepoAddRequest struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) apiHelmRepoAdd(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	var body helmRepoAddRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.URL == "" {
		s.render.Error(w, http.StatusBadRequest, "name and url required")
		return
	}
	args := []string{"repo", "add", body.Name, body.URL, "--force-update"}
	if body.Username != "" {
		args = append(args, "--username", body.Username)
	}
	if body.Password != "" {
		args = append(args, "--password", body.Password)
	}
	if s.gateForbidden(w, "helm", args) {
		return
	}
	start := time.Now()
	out, code := s.runHelmCaptured(r, args)
	// Redact password before auditing.
	auditArgs := redactHelmPassword(args)
	s.recordWebAudit("helm", auditArgs, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": out, "exit_code": code})
}

func (s *Server) apiHelmRepoRemove(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		s.render.Error(w, http.StatusBadRequest, "repo name required")
		return
	}
	var body struct {
		Yes          bool   `json:"yes"`
		ConfirmToken string `json:"confirm_token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	args := []string{"repo", "remove", name}
	if s.gateForbidden(w, "helm", args) {
		return
	}
	if body.ConfirmToken == "" && !body.Yes {
		t := s.pty.Issue(ptyConfirmEntry{Verb: "helm.repo.remove", Resource: "repo/" + name, Argv: args})
		s.render.JSON(w, http.StatusOK, confirmationResponse{
			Status: "needs_confirmation", Style: "yes_no",
			Prompt: "Remove helm repo " + name + "?",
			Token:  t, TTLSeconds: 30,
		})
		return
	}
	if body.ConfirmToken != "" {
		if _, ok := s.pty.Consume(body.ConfirmToken, ""); !ok {
			s.render.Error(w, http.StatusGone, "confirmation expired")
			return
		}
	}
	start := time.Now()
	out, code := s.runHelmCaptured(r, args)
	s.recordWebAudit("helm", args, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": out, "exit_code": code})
}

func (s *Server) apiHelmRepoUpdate(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	args := []string{"repo", "update"}
	start := time.Now()
	out, code := s.runHelmCaptured(r, args)
	s.recordWebAudit("helm", args, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": out, "exit_code": code})
}

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
