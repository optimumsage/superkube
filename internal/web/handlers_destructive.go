package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/optimumsage/superkube/internal/kubectl"
)

// destructiveRequest is the common JSON body shape for delete/scale/rollout
// /drain/cordon. Not every field applies to every verb — handlers use only
// what they need.
type destructiveRequest struct {
	Kind         string `json:"kind"`           // resource kind (pod, deployment, node, ...)
	Name         string `json:"name"`           // resource name
	Namespace    string `json:"namespace"`      // overrides session ns when set
	Replicas     *int   `json:"replicas"`       // scale
	Action       string `json:"action"`         // rollout: undo|restart|pause|resume|status|history
	All          bool   `json:"all"`            // delete --all
	Force        bool   `json:"force"`          // drain --force etc.
	IgnoreDS     bool   `json:"ignore_ds"`      // drain --ignore-daemonsets
	DeleteEmpty  bool   `json:"delete_empty"`   // drain --delete-emptydir-data
	Yes          bool   `json:"yes"`            // bypass confirmation (mirrors --yes)
	ConfirmToken string `json:"confirm_token"`  // returned from a previous needs_confirmation
	ConfirmValue string `json:"confirm_value"`  // typed value the user entered
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

// gateForbidden returns true and writes the response when policy forbids the
// operation outright. Forbid is absolute — no token bypasses it, mirroring
// the CLI's behavior (only editing config.yaml unblocks).
func (s *Server) gateForbidden(w http.ResponseWriter, verb string, args []string) bool {
	rule, blocked := s.deps.Policy().IsForbidden(verb, args)
	if !blocked {
		return false
	}
	bannerText, bannerKind := s.deps.Banner()
	s.render.JSON(w, http.StatusForbidden, confirmationResponse{
		Status:       "blocked",
		Prompt:       "policy forbids this operation",
		Detail:       "Edit config.yaml to remove the forbid rule before retrying.",
		ForbidReason: rule,
		BannerText:   bannerText,
		BannerKind:   bannerKind,
	})
	return true
}

// runKubectlCaptured runs kubectl with args and returns combined output + the
// exit code. Used by every destructive handler so they share one logging /
// audit shape.
func (s *Server) runKubectlCaptured(r *http.Request, args []string) (string, int) {
	sess := s.readSession(r)
	full := args
	if !hasFlagN(args) {
		full = sess.prependGlobalFlags(args)
	} else {
		full = sess.prependGlobalFlagsNoNS(args)
	}
	var out bytes.Buffer
	err := s.deps.Runner.Run(r.Context(), full, kubectl.RunOpts{Stdout: &out, Stderr: &out})
	if err != nil {
		return out.String(), exitCodeOf(err)
	}
	return out.String(), 0
}

// --- delete ----------------------------------------------------------------

func (s *Server) apiDelete(w http.ResponseWriter, r *http.Request) {
	var body destructiveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.render.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	args := buildDeleteArgs(body)
	if s.gateForbidden(w, "delete", args) {
		return
	}

	// Confirmation step.
	if body.ConfirmToken != "" {
		if _, ok := s.pty.Consume(body.ConfirmToken, body.ConfirmValue); !ok {
			s.render.Error(w, http.StatusGone, "confirmation expired or did not match")
			return
		}
	} else if !body.Yes {
		// Choose style: --all → typed phrase "DELETE"; one name → typed name.
		bannerText, bannerKind := s.deps.Banner()
		resp := confirmationResponse{
			Status: "needs_confirmation",
			BannerText: bannerText, BannerKind: bannerKind,
		}
		if body.All {
			resp.Style = "typed_phrase"
			resp.Prompt = fmt.Sprintf("Type DELETE to delete every %s in namespace %s", body.Kind, body.Namespace)
			resp.Expect = "DELETE"
		} else {
			resp.Style = "typed_name"
			resp.Prompt = "Type the resource name to delete"
			resp.Expect = body.Name
		}
		resp.Token = s.pty.Issue(ptyConfirmEntry{Verb: "delete", Resource: body.Kind + "/" + body.Name, Expect: resp.Expect, Argv: args})
		resp.TTLSeconds = 30
		s.render.JSON(w, http.StatusOK, resp)
		return
	}

	start := time.Now()
	output, code := s.runKubectlCaptured(r, args)
	s.recordWebAudit("delete", args, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": output, "exit_code": code})
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Replicas == nil {
		s.render.Error(w, http.StatusBadRequest, "kind, name, replicas required")
		return
	}
	args := []string{"scale", body.Kind + "/" + body.Name, "--replicas=" + strconv.Itoa(*body.Replicas)}
	if body.Namespace != "" {
		args = append(args, "-n", body.Namespace)
	}
	if s.gateForbidden(w, "scale", args) {
		return
	}
	// Only scaling to zero triggers confirmation (CLI parity).
	if *body.Replicas == 0 {
		if body.ConfirmToken == "" && !body.Yes {
			t := s.pty.Issue(ptyConfirmEntry{Verb: "scale", Resource: body.Kind + "/" + body.Name, Argv: args})
			s.render.JSON(w, http.StatusOK, confirmationResponse{
				Status: "needs_confirmation",
				Style:  "yes_no",
				Prompt: fmt.Sprintf("Scale %s/%s to zero replicas?", body.Kind, body.Name),
				Detail: "This will gracefully terminate every pod owned by the workload.",
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
	}
	start := time.Now()
	output, code := s.runKubectlCaptured(r, args)
	s.recordWebAudit("scale", args, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": output, "exit_code": code})
}

// --- rollout ---------------------------------------------------------------

func (s *Server) apiRollout(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if !rolloutActions[action] {
		s.render.Error(w, http.StatusBadRequest, "unknown rollout action")
		return
	}
	var body destructiveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.render.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	args := []string{"rollout", action, body.Kind + "/" + body.Name}
	if body.Namespace != "" {
		args = append(args, "-n", body.Namespace)
	}
	// undo is destructive; require typed name (CLI parity).
	if action == "undo" {
		if s.gateForbidden(w, "rollout", args) {
			return
		}
		if body.ConfirmToken == "" && !body.Yes {
			t := s.pty.Issue(ptyConfirmEntry{Verb: "rollout", Resource: body.Kind + "/" + body.Name, Expect: body.Name, Argv: args})
			s.render.JSON(w, http.StatusOK, confirmationResponse{
				Status: "needs_confirmation", Style: "typed_name",
				Prompt: "Type the workload name to roll back",
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
	}
	start := time.Now()
	output, code := s.runKubectlCaptured(r, args)
	s.recordWebAudit("rollout", args, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": output, "exit_code": code})
}

var rolloutActions = map[string]bool{
	"status": true, "history": true, "restart": true,
	"pause": true, "resume": true, "undo": true,
}

// --- drain / cordon --------------------------------------------------------

func (s *Server) apiDrain(w http.ResponseWriter, r *http.Request) {
	var body destructiveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		s.render.Error(w, http.StatusBadRequest, "name required")
		return
	}
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
	if s.gateForbidden(w, "drain", args) {
		return
	}
	if body.ConfirmToken == "" && !body.Yes {
		t := s.pty.Issue(ptyConfirmEntry{Verb: "drain", Resource: "node/" + body.Name, Expect: body.Name, Argv: args})
		s.render.JSON(w, http.StatusOK, confirmationResponse{
			Status: "needs_confirmation", Style: "typed_name",
			Prompt: "Type the node name to drain it",
			Detail: "This will evict every pod from the node.",
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
	output, code := s.runKubectlCaptured(r, args)
	s.recordWebAudit("drain", args, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": output, "exit_code": code})
}

func (s *Server) apiCordon(w http.ResponseWriter, r *http.Request)   { s.cordonLike(w, r, "cordon") }
func (s *Server) apiUncordon(w http.ResponseWriter, r *http.Request) { s.cordonLike(w, r, "uncordon") }

func (s *Server) cordonLike(w http.ResponseWriter, r *http.Request, verb string) {
	var body destructiveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		s.render.Error(w, http.StatusBadRequest, "name required")
		return
	}
	args := []string{verb, body.Name}
	if s.gateForbidden(w, verb, args) {
		return
	}
	if body.ConfirmToken == "" && !body.Yes {
		t := s.pty.Issue(ptyConfirmEntry{Verb: verb, Resource: "node/" + body.Name, Argv: args})
		s.render.JSON(w, http.StatusOK, confirmationResponse{
			Status: "needs_confirmation", Style: "yes_no",
			Prompt: strings.Title(verb) + " node " + body.Name + "?",
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
	output, code := s.runKubectlCaptured(r, args)
	s.recordWebAudit(verb, args, code, time.Since(start))
	s.render.JSON(w, statusForExit(code), map[string]any{"output": output, "exit_code": code})
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
