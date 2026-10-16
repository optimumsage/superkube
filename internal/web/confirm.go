package web

import (
	"net/http"

	"github.com/optimumsage/superkube/internal/guardrail"
)

// policyFor resolves the guardrail policy for the session's kube context.
// Policy is per-context (config.yaml contexts: sections), so it must follow
// the UI's context switch rather than the context `sk web` started in. A nil
// Deps.PolicyFor yields the empty policy (tests / embedders); an error means
// "could not evaluate policy" and callers must refuse the operation.
func (s *Server) policyFor(sess session) (guardrail.Policy, error) {
	if s.deps.PolicyFor == nil {
		return guardrail.Policy{}, nil
	}
	return s.deps.PolicyFor(sess.Context)
}

// bannerFor returns the prod-context banner for the session's context.
func (s *Server) bannerFor(sess session) (text, kind string) {
	if s.deps.BannerFor == nil {
		return "", ""
	}
	return s.deps.BannerFor(sess.Context)
}

// policyUnavailable writes the refusal used when config.yaml cannot be parsed.
// Treating that as "no policy" would silently disable forbid rules.
func (s *Server) policyUnavailable(w http.ResponseWriter, sess session, err error) {
	bannerText, bannerKind := s.bannerFor(sess)
	s.render.JSON(w, http.StatusServiceUnavailable, confirmationResponse{
		Status:     "blocked",
		Prompt:     "cannot evaluate guardrail policy; refusing the operation",
		Detail:     "Fix config.yaml (" + err.Error() + ") and retry.",
		BannerText: bannerText,
		BannerKind: bannerKind,
	})
}

// gateForbidden returns true and writes the response when the operation must
// not proceed: either policy forbids it outright (no token or --yes bypasses
// that, mirroring the CLI where only editing config.yaml unblocks) or the
// policy could not be loaded. It must run on every commit as well as every
// preview — the context may have changed between the two.
func (s *Server) gateForbidden(w http.ResponseWriter, sess session, verb string, args []string) bool {
	pol, err := s.policyFor(sess)
	if err != nil {
		s.policyUnavailable(w, sess, err)
		return true
	}
	rule, blocked := pol.IsForbidden(verb, args)
	if !blocked {
		return false
	}
	bannerText, bannerKind := s.bannerFor(sess)
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

// confirmOp describes one guarded operation for gateConfirm.
type confirmOp struct {
	Verb     string // bound into the token, e.g. "delete", "helm.rollback"
	Resource string // for audit/UI
	Name     string // value the user types when policy upgrades yes/no to typed
	Style    string // base style: "yes_no" | "typed_name" | "typed_phrase"
	Expect   string // typed value for typed_name / typed_phrase
	Prompt   string
	Detail   string
	Digest   string // binds the token to this exact operation
	NoSkip   bool   // server-side --yes may not skip (e.g. delete --all)
}

// effectiveStyle applies per-context policy to a base confirmation style.
// RequireTypedConfirm upgrades yes/no to typing the resource name, exactly as
// guardrail.Policy.UpgradeStyle does for the CLI.
func effectiveStyle(pol guardrail.Policy, style, name string) (outStyle, expect string) {
	if style == "yes_no" && pol.UpgradeStyle("yesno") == "typed-phrase" && name != "" {
		return "typed_name", name
	}
	return style, ""
}

// upgradeOp returns op with Style/Expect/Prompt adjusted for policy.
func upgradeOp(pol guardrail.Policy, op confirmOp) confirmOp {
	if st, expect := effectiveStyle(pol, op.Style, op.Name); st != op.Style {
		op.Style = st
		op.Expect = expect
		op.Prompt += " Type " + expect + " to confirm."
	}
	return op
}

// gateConfirm runs the single-request confirmation flow shared by delete,
// scale, rollout, drain, cordon and helm rollback/uninstall/repo-remove.
//
//   - No token: unless the server itself was started with --yes (never the
//     client — a JSON "yes" is ignored), issue a token bound to op and answer
//     needs_confirmation.
//   - Token: it must have been minted for this verb, context and digest, and
//     the typed value must match.
//
// It returns true when the caller may execute; otherwise the response has
// already been written.
func (s *Server) gateConfirm(w http.ResponseWriter, sess session, op confirmOp, token, typed string) bool {
	pol, err := s.policyFor(sess)
	if err != nil {
		s.policyUnavailable(w, sess, err)
		return false
	}
	op = upgradeOp(pol, op)
	if token != "" {
		_, ok := s.pty.Consume(token, typed, op.Verb, sess.Context, op.Digest)
		if !ok {
			s.render.Error(w, http.StatusGone, "confirmation expired or did not match")
		}
		return ok
	}
	if s.deps.Yes && !op.NoSkip {
		return true
	}
	bannerText, bannerKind := s.bannerFor(sess)
	tok := s.pty.Issue(ptyConfirmEntry{
		Verb: op.Verb, Context: sess.Context, Resource: op.Resource,
		Expect: op.Expect, Digest: op.Digest,
	})
	s.render.JSON(w, http.StatusOK, confirmationResponse{
		Status: "needs_confirmation", Style: op.Style,
		Prompt: op.Prompt, Detail: op.Detail, Expect: op.Expect,
		Token: tok, TTLSeconds: 30,
		BannerText: bannerText, BannerKind: bannerKind,
	})
	return false
}

// consumePreview validates the token for the two-step preview→commit flows
// (apply, edit, helm install/upgrade). It writes 410 on failure.
func (s *Server) consumePreview(w http.ResponseWriter, sess session, token, typed, verb, digest string) (ptyConfirmEntry, bool) {
	e, ok := s.pty.Consume(token, typed, verb, sess.Context, digest)
	if !ok {
		s.render.Error(w, http.StatusGone, "confirmation expired or did not match the previewed operation")
	}
	return e, ok
}
