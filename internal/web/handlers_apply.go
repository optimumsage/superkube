package web

import (
	"bytes"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/kubectl"
)

// applyArgsForPolicy is the argv shown to policy checks for an inline
// manifest, so `forbid: ["apply"]` style rules match the web flow too.
var applyArgsForPolicy = []string{"-f", "<inline>"}

// yamlDigest binds a token to the exact manifest text, the verb and the
// session's target (context is checked separately by the store; namespace is
// included because kubectl injects -n from the session).
func yamlDigest(sess session, verb, target, yamlText string) string {
	return digestOf(verb, sess.Namespace, target, yamlText)
}

// applyForbidden runs the forbid gates for an inline manifest against EVERY
// document it contains: the generic `apply -f <inline>` shape plus, per
// document, the lower-cased kind, kind/name and the effective namespace, so a
// rule like `forbid: ["apply secret"]` or `["apply -n kube-system"]` cannot be
// dodged by tucking the object behind a second `---` document.
func (s *Server) applyForbidden(w http.ResponseWriter, sess session, yamlText string) bool {
	docs, err := parseManifestDocs(yamlText)
	if err != nil {
		s.render.Error(w, http.StatusBadRequest, err.Error())
		return true
	}
	if len(docs) == 0 {
		s.render.Error(w, http.StatusBadRequest, "yaml required")
		return true
	}
	if s.gateForbidden(w, sess, "apply", applyArgsForPolicy) {
		return true
	}
	for _, d := range docs {
		ns := d.Namespace
		if ns == "" {
			ns = sess.Namespace
		}
		args := append(append([]string{}, applyArgsForPolicy...),
			strings.ToLower(d.Kind), d.Kind, d.Name, strings.ToLower(d.Kind)+"/"+d.Name, "-n", ns)
		if s.gateForbidden(w, sess, "apply", args) {
			return true
		}
	}
	return false
}

// previewYAML runs `kubectl diff` for yamlText and, when there are changes,
// issues a confirmation token bound to (verb, context, digest) that also
// carries the previewed YAML as its Payload. The commit executes that stored
// payload, never whatever the client resubmits.
func (s *Server) previewYAML(w http.ResponseWriter, r *http.Request, sess session, verb, resource, digest, yamlText, expectName string) {
	pol, perr := s.policyFor(sess)
	if perr != nil {
		s.policyUnavailable(w, sess, perr)
		return
	}
	tmp, cleanup, err := writeTempYAML(yamlText)
	if err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer cleanup()

	args := sess.prependGlobalFlags([]string{"-f", tmp})

	var buf bytes.Buffer
	result, err := guardrail.PreviewApply(r.Context(), s.deps.Runner, args, &buf)
	if err != nil {
		s.render.Error(w, http.StatusBadGateway, err.Error())
		return
	}

	if !result.HasChanges {
		s.render.JSON(w, http.StatusOK, map[string]any{
			"status": "no_changes",
			"diff":   buf.String(),
		})
		return
	}

	// Typed confirmation under require_typed_confirm: the resource name for
	// edit, the word APPLY for a free-form manifest.
	style, expect := effectiveStyle(pol, "yes_no", expectName)
	token := s.pty.Issue(ptyConfirmEntry{
		Verb:     verb,
		Context:  sess.Context,
		Resource: resource,
		Digest:   digest,
		Payload:  yamlText,
		Expect:   expect,
	})

	resp := map[string]any{
		"status":        "needs_confirmation",
		"diff":          buf.String(),
		"diff_html":     diffToHTML(buf.String()),
		"confirm_token": token,
		"ttl":           30,
		"style":         style,
	}
	if expect != "" {
		resp["expect"] = expect
		resp["prompt"] = "Type " + expect + " to confirm."
	}
	s.render.JSON(w, http.StatusOK, resp)
}

// applyYAML writes payload to a fresh tmp file (the preview's may have been
// GC'd) and runs `kubectl apply -f`.
func (s *Server) applyYAML(r *http.Request, sess session, payload string) (out string, exitCode int, err error) {
	tmp, cleanup, err := writeTempYAML(payload)
	if err != nil {
		return "", 0, err
	}
	defer cleanup()

	args := sess.prependGlobalFlags([]string{"apply", "-f", tmp})
	var buf bytes.Buffer
	runErr := s.deps.Runner.Run(r.Context(), args, kubectl.RunOpts{Stdout: &buf, Stderr: &buf})
	if runErr != nil {
		exitCode = exitCodeOf(runErr)
	}
	return buf.String(), exitCode, nil
}

// apiApplyPreview accepts a YAML manifest, writes it to a temp file, and runs
// `kubectl diff -f tmp` so the user can see exactly what will change. On
// success we issue a single-use confirm token bound to the manifest's digest;
// the client renders the diff then re-submits with the token to
// apiApplyCommit.
func (s *Server) apiApplyPreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML string `json:"yaml"`
	}
	if err := s.decodeJSON(w, r, &body); err != nil || strings.TrimSpace(body.YAML) == "" {
		s.render.Error(w, http.StatusBadRequest, "yaml required")
		return
	}
	sess := s.readSession(r)
	if s.applyForbidden(w, sess, body.YAML) {
		return
	}
	s.previewYAML(w, r, sess, "apply", "manifest", yamlDigest(sess, "apply", "", body.YAML), body.YAML, "APPLY")
}

// apiApplyCommit consumes the token from apiApplyPreview and runs the real
// apply of exactly the previewed manifest. The resubmitted YAML must hash to
// the digest recorded at preview time, and the policy gate is re-run (the
// session context may have changed since the preview).
func (s *Server) apiApplyCommit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML         string `json:"yaml"`
		Token        string `json:"confirm_token"`
		ConfirmValue string `json:"confirm_value"`
	}
	if err := s.decodeJSON(w, r, &body); err != nil {
		s.render.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	sess := s.readSession(r)
	if s.applyForbidden(w, sess, body.YAML) {
		return
	}
	entry, ok := s.consumePreview(w, sess, body.Token, body.ConfirmValue, "apply", yamlDigest(sess, "apply", "", body.YAML))
	if !ok {
		return
	}

	start := time.Now()
	out, exitCode, err := s.applyYAML(r, sess, entry.Payload)
	if err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordWebAudit(r, "apply", applyArgsForPolicy, exitCode, time.Since(start))

	if exitCode != 0 {
		s.render.JSON(w, http.StatusBadGateway, map[string]any{
			"status": "error",
			"output": out,
		})
		return
	}
	s.render.JSON(w, http.StatusOK, map[string]any{
		"status": "applied",
		"output": out,
	})
}

// writeTempYAML drops content into a temp file and returns the path + a
// cleanup func. We persist the file long enough for the kubectl child to
// open it and read it.
func writeTempYAML(content string) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "sk-apply-*.yaml")
	if err != nil {
		return "", nil, err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	_ = f.Close()
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}

// diffToHTML converts a raw kubectl diff into HTML with per-line classes that
// our CSS colorizes. We keep the rendering server-side so xss-paranoid clients
// don't have to parse ANSI.
func diffToHTML(raw string) string {
	var sb strings.Builder
	sb.Grow(len(raw) + 64)
	for _, line := range strings.SplitAfter(raw, "\n") {
		class := "diff-line"
		stripped := strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(stripped, "+++") || strings.HasPrefix(stripped, "---"):
			class += " head"
		case strings.HasPrefix(stripped, "@@"):
			class += " hunk"
		case strings.HasPrefix(stripped, "+"):
			class += " add"
		case strings.HasPrefix(stripped, "-"):
			class += " del"
		}
		sb.WriteString(`<span class="` + class + `">`)
		sb.WriteString(htmlEscape(line))
		sb.WriteString(`</span>`)
	}
	return sb.String()
}

func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return r.Replace(s)
}

// exitCodeOf returns kubectl's exit code, or 1 for non-ExitCodeError values.
func exitCodeOf(err error) int {
	var ee *kubectl.ExitCodeError
	if asErrAs(err, &ee) {
		return ee.Code
	}
	return 1
}
