package web

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/optimumsage/superkube/internal/kubectl"
)

// destructiveRequest is the common JSON body shape for delete/scale/rollout
// /drain/cordon. Not every field applies to every verb — handlers use only
// what they need.
//
// There is deliberately no client-controlled "yes" field: confirmation can
// only be skipped by the server operator (`sk web --yes`), never by a request
// body. Unknown JSON keys (including a stray "yes") are ignored.
type destructiveRequest struct {
	Kind         string `json:"kind"`          // resource kind (pod, deployment, node, ...)
	Name         string `json:"name"`          // resource name
	Namespace    string `json:"namespace"`     // overrides session ns when set
	Replicas     *int   `json:"replicas"`      // scale
	Action       string `json:"action"`        // rollout: undo|restart|pause|resume|status|history
	Revision     int    `json:"revision"`      // rollout undo --to-revision
	All          bool   `json:"all"`           // delete --all
	Force        bool   `json:"force"`         // drain --force etc.
	IgnoreDS     bool   `json:"ignore_ds"`     // drain --ignore-daemonsets
	DeleteEmpty  bool   `json:"delete_empty"`  // drain --delete-emptydir-data
	ConfirmToken string `json:"confirm_token"` // returned from a previous needs_confirmation
	ConfirmValue string `json:"confirm_value"` // typed value the user entered
}

// confirmationResponse is what we return when the operation needs typed
// confirmation. The client renders a modal that mirrors the CLI prompt.
type confirmationResponse struct {
	Status       string `json:"status"` // "needs_confirmation" | "blocked" | ...
	Style        string `json:"style"`  // "yes_no" | "typed_name" | "typed_phrase"
	Prompt       string `json:"prompt"`
	Detail       string `json:"detail,omitempty"`
	Expect       string `json:"expect,omitempty"` // typed_name/phrase only
	Token        string `json:"token,omitempty"`
	TTLSeconds   int    `json:"ttl,omitempty"`
	BannerText   string `json:"banner_text,omitempty"`
	BannerKind   string `json:"banner_kind,omitempty"`
	ForbidReason string `json:"forbid_reason,omitempty"`
}

// runKubectlCaptured runs kubectl with args and returns combined output + the
// exit code. Used by every destructive handler so they share one logging /
// audit shape.
func (s *Server) runKubectlCaptured(r *http.Request, args []string) (string, int) {
	full := s.readSession(r).kubectlArgs(args)
	var out bytes.Buffer
	err := s.deps.Runner.Run(r.Context(), full, kubectl.RunOpts{Stdout: &out, Stderr: &out})
	if err != nil {
		return out.String(), exitCodeOf(err)
	}
	return out.String(), 0
}

// argvDigest binds a confirmation token to the exact argv (plus the session
// namespace that kubectl injects when argv carries no -n of its own).
func argvDigest(sess session, verb string, args []string) string {
	parts := append([]string{verb, sess.Namespace}, args...)
	return digestOf(parts...)
}

// badToken reports values that must never reach kubectl's argv as a resource
// kind / name / namespace: empty or flag-shaped.
func badToken(vals ...string) bool {
	for _, v := range vals {
		if v == "" || v[0] == '-' {
			return true
		}
	}
	return false
}

// finish runs args, audits, and writes the standard result body.
func (s *Server) finish(w http.ResponseWriter, r *http.Request, verb string, args []string) {
	start := time.Now()
	output, code := s.runKubectlCaptured(r, args)
	s.recordWebAudit(r, verb, args, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": output, "exit_code": code})
}

// --- delete ----------------------------------------------------------------

func (s *Server) apiDelete(w http.ResponseWriter, r *http.Request) {
	var body destructiveRequest
	if err := s.decodeJSON(w, r, &body); err != nil {
		s.render.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if badToken(body.Kind) || (!body.All && badToken(body.Name)) || (body.Namespace != "" && badToken(body.Namespace)) {
		s.render.Error(w, http.StatusBadRequest, "kind and name required")
		return
	}
	sess := s.readSession(r)
	args := buildDeleteArgs(body)
	if s.gateForbidden(w, sess, "delete", args) {
		return
	}

	// --all → typed phrase "DELETE" that the server-side --yes cannot skip;
	// one name → typed name, and the token is bound to that exact argv so
	// confirming "foo" can never delete a different object.
	op := confirmOp{Verb: "delete", Resource: body.Kind + "/" + body.Name, Digest: argvDigest(sess, "delete", args)}
	if body.All {
		ns := body.Namespace
		if ns == "" {
			ns = sess.Namespace
		}
		op.Style = "typed_phrase"
		op.Prompt = fmt.Sprintf("Type DELETE to delete every %s in namespace %s", body.Kind, ns)
		op.Expect = "DELETE"
		op.NoSkip = true
	} else {
		op.Style = "typed_name"
		op.Prompt = "Type the resource name to delete"
		op.Expect = body.Name
	}
	if !s.gateConfirm(w, sess, op, body.ConfirmToken, body.ConfirmValue) {
		return
	}
	s.finish(w, r, "delete", args)
}

func buildDeleteArgs(b destructiveRequest) []string {
	args := []string{"delete"}
	if b.All {
		args = append(args, b.Kind, "--all")
	} else {
		args = append(args, b.Kind, b.Name)
	}
	if b.Namespace != "" {
		args = append(args, "-n", b.Namespace)
	}
	return args
}

// --- scale -----------------------------------------------------------------

func (s *Server) apiScale(w http.ResponseWriter, r *http.Request) {
	var body destructiveRequest
	if err := s.decodeJSON(w, r, &body); err != nil || body.Replicas == nil || *body.Replicas < 0 {
		s.render.Error(w, http.StatusBadRequest, "kind, name, replicas required")
		return
	}
	if badToken(body.Kind, body.Name) || (body.Namespace != "" && badToken(body.Namespace)) {
		s.render.Error(w, http.StatusBadRequest, "kind, name, replicas required")
		return
	}
	sess := s.readSession(r)
	args := []string{"scale", body.Kind + "/" + body.Name, "--replicas=" + strconv.Itoa(*body.Replicas)}
	if body.Namespace != "" {
		args = append(args, "-n", body.Namespace)
	}
	if s.gateForbidden(w, sess, "scale", args) {
		return
	}
	// Only scaling to zero triggers confirmation (CLI parity). Policy may
	// upgrade the yes/no to typing the workload name.
	if *body.Replicas == 0 {
		op := confirmOp{
			Verb: "scale", Resource: body.Kind + "/" + body.Name, Name: body.Name,
			Style:  "yes_no",
			Prompt: fmt.Sprintf("Scale %s/%s to zero replicas?", body.Kind, body.Name),
			Detail: "This will gracefully terminate every pod owned by the workload.",
			Digest: argvDigest(sess, "scale", args),
		}
		if !s.gateConfirm(w, sess, op, body.ConfirmToken, body.ConfirmValue) {
			return
		}
	}
	s.finish(w, r, "scale", args)
}

// --- rollout ---------------------------------------------------------------

func (s *Server) apiRollout(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if !rolloutActions[action] {
		s.render.Error(w, http.StatusBadRequest, "unknown rollout action")
		return
	}
	var body destructiveRequest
	if err := s.decodeJSON(w, r, &body); err != nil {
		s.render.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if badToken(body.Kind, body.Name) || (body.Namespace != "" && badToken(body.Namespace)) {
		s.render.Error(w, http.StatusBadRequest, "kind and name required")
		return
	}
	sess := s.readSession(r)
	args := []string{"rollout", action, body.Kind + "/" + body.Name}
	if body.Namespace != "" {
		args = append(args, "-n", body.Namespace)
	}
	if action == "undo" && body.Revision > 0 {
		args = append(args, fmt.Sprintf("--to-revision=%d", body.Revision))
	}
	// undo is destructive; require typed name (CLI parity).
	if action == "undo" {
		if s.gateForbidden(w, sess, "rollout", args) {
			return
		}
		op := confirmOp{
			Verb: "rollout", Resource: body.Kind + "/" + body.Name,
			Style: "typed_name", Expect: body.Name,
			Prompt: "Type the workload name to roll back",
			Digest: argvDigest(sess, "rollout", args),
		}
		if !s.gateConfirm(w, sess, op, body.ConfirmToken, body.ConfirmValue) {
			return
		}
	}
	s.finish(w, r, "rollout", args)
}

var rolloutActions = map[string]bool{
	"status": true, "history": true, "restart": true,
	"pause": true, "resume": true, "undo": true,
}

// --- drain / cordon --------------------------------------------------------

func (s *Server) apiDrain(w http.ResponseWriter, r *http.Request) {
	var body destructiveRequest
	if err := s.decodeJSON(w, r, &body); err != nil || badToken(body.Name) {
		s.render.Error(w, http.StatusBadRequest, "name required")
		return
	}
	sess := s.readSession(r)
	args := []string{"drain", body.Name}
	if body.Force {
		args = append(args, "--force")
	}
	if body.IgnoreDS {
		args = append(args, "--ignore-daemonsets")
	}
	if body.DeleteEmpty {
		args = append(args, "--delete-emptydir-data")
	}
	if s.gateForbidden(w, sess, "drain", args) {
		return
	}
	op := confirmOp{
		Verb: "drain", Resource: "node/" + body.Name,
		Style: "typed_name", Expect: body.Name,
		Prompt: "Type the node name to drain it",
		Detail: "This will evict every pod from the node.",
		Digest: argvDigest(sess, "drain", args),
	}
	if !s.gateConfirm(w, sess, op, body.ConfirmToken, body.ConfirmValue) {
		return
	}
	s.finish(w, r, "drain", args)
}

func (s *Server) apiCordon(w http.ResponseWriter, r *http.Request)   { s.cordonLike(w, r, "cordon") }
func (s *Server) apiUncordon(w http.ResponseWriter, r *http.Request) { s.cordonLike(w, r, "uncordon") }

func (s *Server) cordonLike(w http.ResponseWriter, r *http.Request, verb string) {
	var body destructiveRequest
	if err := s.decodeJSON(w, r, &body); err != nil || badToken(body.Name) {
		s.render.Error(w, http.StatusBadRequest, "name required")
		return
	}
	sess := s.readSession(r)
	args := []string{verb, body.Name}
	if s.gateForbidden(w, sess, verb, args) {
		return
	}
	op := confirmOp{
		Verb: verb, Resource: "node/" + body.Name, Name: body.Name,
		Style:  "yes_no",
		Prompt: verbTitle(verb) + " node " + body.Name + "?",
		Digest: argvDigest(sess, verb, args),
	}
	if !s.gateConfirm(w, sess, op, body.ConfirmToken, body.ConfirmValue) {
		return
	}
	s.finish(w, r, verb, args)
}

// verbTitle upper-cases the first letter of an ASCII verb (strings.Title is
// deprecated and Unicode-aware behavior is not needed here).
func verbTitle(v string) string {
	if v == "" {
		return v
	}
	b := []byte(v)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	return string(b)
}

// statusForExit picks an HTTP status that reflects whether kubectl exited
// successfully. Non-zero kubectl exit is surfaced as 502 so the client side's
// generic XHR error handler can flag it; the body still contains the kubectl
// output verbatim.
func statusForExit(code int) int {
	if code == 0 {
		return http.StatusOK
	}
	return http.StatusBadGateway
}
