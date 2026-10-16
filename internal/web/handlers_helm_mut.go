package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/optimumsage/superkube/internal/helm"
)

// handlers_helm_mut.go holds the helm mutations: rollback / uninstall /
// upgrade / install / repo add+remove+update. They share the guarded flow of
// the kubectl destructive handlers: forbid gate → confirmation bound to the
// exact operation → run → audit. As there, no request-body field can skip a
// confirmation; only the server-side `sk web --yes` can.

// helmPreviewDigest binds a preview token to the whole install/upgrade
// request (chart, version, values text, flags). The argv itself cannot be
// hashed because it contains a per-call temp values file path.
func helmPreviewDigest(sess session, verb string, req any) string {
	b, _ := json.Marshal(req)
	return digestOf(verb, sess.Namespace, string(b))
}

type helmRollbackRequest struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Revision     int    `json:"revision"`
	Wait         bool   `json:"wait"`
	Force        bool   `json:"force"`
	ConfirmToken string `json:"confirm_token"`
	ConfirmValue string `json:"confirm_value"`
}

func (s *Server) apiHelmRollback(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	var body helmRollbackRequest
	if err := s.decodeJSON(w, r, &body); err != nil || badToken(body.Name, body.Namespace) || body.Revision <= 0 {
		s.render.Error(w, http.StatusBadRequest, "name, namespace, revision required")
		return
	}
	sess := s.readSession(r)
	args := []string{"rollback", body.Name, strconv.Itoa(body.Revision), "-n", body.Namespace}
	if body.Wait {
		args = append(args, "--wait")
	}
	if body.Force {
		args = append(args, "--force")
	}
	if s.gateForbidden(w, sess, "helm", args) {
		return
	}
	op := confirmOp{
		Verb: "helm.rollback", Resource: body.Namespace + "/" + body.Name, Name: body.Name,
		Style:  "yes_no",
		Prompt: fmt.Sprintf("Roll back %s to revision %d?", body.Name, body.Revision),
		Detail: "Helm will re-apply the chart manifests from the chosen revision.",
		Digest: argvDigest(sess, "helm.rollback", args),
	}
	if !s.gateConfirm(w, sess, op, body.ConfirmToken, body.ConfirmValue) {
		return
	}
	s.finishHelm(w, r, args)
}

// finishHelm runs helm args, audits, and writes the standard result body.
func (s *Server) finishHelm(w http.ResponseWriter, r *http.Request, args []string) {
	start := time.Now()
	out, code := s.runHelmCaptured(r, args)
	s.recordWebAudit(r, "helm", args, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": out, "exit_code": code})
}

type helmUninstallRequest struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	KeepHistory  bool   `json:"keep_history"`
	ConfirmToken string `json:"confirm_token"`
	ConfirmValue string `json:"confirm_value"`
}

func (s *Server) apiHelmUninstall(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	var body helmUninstallRequest
	if err := s.decodeJSON(w, r, &body); err != nil || badToken(body.Name, body.Namespace) {
		s.render.Error(w, http.StatusBadRequest, "name, namespace required")
		return
	}
	sess := s.readSession(r)
	args := []string{"uninstall", body.Name, "-n", body.Namespace}
	if body.KeepHistory {
		args = append(args, "--keep-history")
	}
	if s.gateForbidden(w, sess, "helm", args) {
		return
	}
	op := confirmOp{
		Verb: "helm.uninstall", Resource: body.Namespace + "/" + body.Name,
		Style: "typed_name", Expect: body.Name,
		Prompt: "Type the release name to uninstall",
		Detail: "This will remove every resource managed by the release.",
		Digest: argvDigest(sess, "helm.uninstall", args),
	}
	if !s.gateConfirm(w, sess, op, body.ConfirmToken, body.ConfirmValue) {
		return
	}
	s.finishHelm(w, r, args)
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
	ConfirmToken    string `json:"confirm_token"`
	ConfirmValue    string `json:"confirm_value"`
}

// helmPreviewToken issues the preview token and builds the response, applying
// per-context typed-confirm policy to the commit step.
func (s *Server) helmPreviewToken(w http.ResponseWriter, sess session, verb, name, resource, digest, rendered string) {
	pol, err := s.policyFor(sess)
	if err != nil {
		s.policyUnavailable(w, sess, err)
		return
	}
	style, expect := effectiveStyle(pol, "yes_no", name)
	t := s.pty.Issue(ptyConfirmEntry{Verb: verb, Context: sess.Context, Resource: resource, Expect: expect, Digest: digest})
	resp := map[string]any{
		"status":        "needs_confirmation",
		"rendered":      rendered,
		"confirm_token": t,
		"ttl":           30,
		"style":         style,
	}
	if expect != "" {
		resp["expect"] = expect
		resp["prompt"] = "Type " + expect + " to confirm."
	}
	s.render.JSON(w, http.StatusOK, resp)
}

// helmCommitGate is the commit-side check shared by install and upgrade: a
// previewed token bound to this exact request, or (server --yes only) none.
func (s *Server) helmCommitGate(w http.ResponseWriter, sess session, verb, token, typed, digest string) bool {
	if token == "" {
		if s.deps.Yes {
			return true
		}
		s.render.Error(w, http.StatusBadRequest, "confirm_token required; preview first")
		return false
	}
	_, ok := s.consumePreview(w, sess, token, typed, verb, digest)
	return ok
}

// apiHelmUpgradePreview runs `helm upgrade --dry-run` and returns the rendered
// manifest so the SPA can show it as a preview before committing.
func (s *Server) apiHelmUpgradePreview(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	var body helmUpgradeRequest
	if err := s.decodeJSON(w, r, &body); err != nil || badToken(body.Name, body.Namespace, body.Chart) {
		s.render.Error(w, http.StatusBadRequest, "name, namespace, chart required")
		return
	}
	sess := s.readSession(r)
	args, cleanup, err := buildUpgradeArgs(body, true)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		s.render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.gateForbidden(w, sess, "helm", args) {
		return
	}
	out, code := s.runHelmCaptured(r, args)
	if code != 0 {
		s.render.JSON(w, statusForExit(code), map[string]any{"error": out, "exit_code": code})
		return
	}
	body.ConfirmToken, body.ConfirmValue = "", ""
	s.helmPreviewToken(w, sess, "helm.upgrade", body.Name, body.Namespace+"/"+body.Name,
		helmPreviewDigest(sess, "helm.upgrade", body), out)
}

func (s *Server) apiHelmUpgradeCommit(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	var body helmUpgradeRequest
	if err := s.decodeJSON(w, r, &body); err != nil || badToken(body.Name, body.Namespace, body.Chart) {
		s.render.Error(w, http.StatusBadRequest, "name, namespace, chart required")
		return
	}
	sess := s.readSession(r)
	args, cleanup, err := buildUpgradeArgs(body, false)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		s.render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.gateForbidden(w, sess, "helm", args) {
		return
	}
	token, typed := body.ConfirmToken, body.ConfirmValue
	body.ConfirmToken, body.ConfirmValue = "", ""
	if !s.helmCommitGate(w, sess, "helm.upgrade", token, typed, helmPreviewDigest(sess, "helm.upgrade", body)) {
		return
	}
	s.finishHelm(w, r, args)
}

// buildUpgradeArgs constructs the helm upgrade argv, writing values YAML to a
// temp file when provided. Caller must call cleanup() (may be nil).
func buildUpgradeArgs(body helmUpgradeRequest, dryRun bool) (args []string, cleanup func(), err error) {
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
		path, c, err := writeTempYAML(body.Values)
		if err != nil {
			return nil, nil, err
		}
		cleanup = c
		args = append(args, "-f", path)
	}
	return args, cleanup, nil
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
	ConfirmToken    string `json:"confirm_token"`
	ConfirmValue    string `json:"confirm_value"`
}

func (s *Server) apiHelmInstallPreview(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	var body helmInstallRequest
	if err := s.decodeJSON(w, r, &body); err != nil || badToken(body.Name, body.Namespace, body.Chart) {
		s.render.Error(w, http.StatusBadRequest, "name, namespace, chart required")
		return
	}
	sess := s.readSession(r)
	args, cleanup, err := buildInstallArgs(body, true)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		s.render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.gateForbidden(w, sess, "helm", args) {
		return
	}
	out, code := s.runHelmCaptured(r, args)
	if code != 0 {
		s.render.JSON(w, statusForExit(code), map[string]any{"error": out, "exit_code": code})
		return
	}
	body.ConfirmToken, body.ConfirmValue = "", ""
	s.helmPreviewToken(w, sess, "helm.install", body.Name, body.Namespace+"/"+body.Name,
		helmPreviewDigest(sess, "helm.install", body), out)
}

func (s *Server) apiHelmInstallCommit(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	var body helmInstallRequest
	if err := s.decodeJSON(w, r, &body); err != nil || badToken(body.Name, body.Namespace, body.Chart) {
		s.render.Error(w, http.StatusBadRequest, "name, namespace, chart required")
		return
	}
	sess := s.readSession(r)
	args, cleanup, err := buildInstallArgs(body, false)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		s.render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.gateForbidden(w, sess, "helm", args) {
		return
	}
	token, typed := body.ConfirmToken, body.ConfirmValue
	body.ConfirmToken, body.ConfirmValue = "", ""
	if !s.helmCommitGate(w, sess, "helm.install", token, typed, helmPreviewDigest(sess, "helm.install", body)) {
		return
	}
	s.finishHelm(w, r, args)
}

func buildInstallArgs(body helmInstallRequest, dryRun bool) (args []string, cleanup func(), err error) {
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
		path, c, err := writeTempYAML(body.Values)
		if err != nil {
			return nil, nil, err
		}
		cleanup = c
		args = append(args, "-f", path)
	}
	return args, cleanup, nil
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
	if err := s.decodeJSON(w, r, &body); err != nil || badToken(body.Name, body.URL) {
		s.render.Error(w, http.StatusBadRequest, "name and url required")
		return
	}
	sess := s.readSession(r)
	args := []string{"repo", "add", body.Name, body.URL, "--force-update"}
	if body.Username != "" {
		args = append(args, "--username", body.Username)
	}
	if body.Password != "" {
		args = append(args, "--password", body.Password)
	}
	if s.gateForbidden(w, sess, "helm", args) {
		return
	}
	start := time.Now()
	out, code := s.runHelmCaptured(r, args)
	// Redact credentials before auditing.
	s.recordWebAudit(r, "helm", redactHelmPassword(args), code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": out, "exit_code": code})
}

func (s *Server) apiHelmRepoRemove(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	name := r.PathValue("name")
	if badToken(name) {
		s.render.Error(w, http.StatusBadRequest, "repo name required")
		return
	}
	var body struct {
		ConfirmToken string `json:"confirm_token"`
		ConfirmValue string `json:"confirm_value"`
	}
	_ = s.decodeJSON(w, r, &body)
	sess := s.readSession(r)
	args := []string{"repo", "remove", name}
	if s.gateForbidden(w, sess, "helm", args) {
		return
	}
	op := confirmOp{
		Verb: "helm.repo.remove", Resource: "repo/" + name, Name: name,
		Style:  "yes_no",
		Prompt: "Remove helm repo " + name + "?",
		Digest: argvDigest(sess, "helm.repo.remove", args),
	}
	if !s.gateConfirm(w, sess, op, body.ConfirmToken, body.ConfirmValue) {
		return
	}
	s.finishHelm(w, r, args)
}

func (s *Server) apiHelmRepoUpdate(w http.ResponseWriter, r *http.Request) {
	if s.helmRequired(w) {
		return
	}
	s.finishHelm(w, r, []string{"repo", "update"})
}
