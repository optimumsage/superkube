package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/kubectl"
)

// apiApplyPreview accepts a YAML manifest, writes it to a temp file, and runs
// `kubectl diff -f tmp` so the user can see exactly what will change. On
// success we issue a single-use confirm token; the client renders the diff
// then re-submits with the token to apiApplyCommit.
func (s *Server) apiApplyPreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML string `json:"yaml"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.YAML) == "" {
		s.render.Error(w, http.StatusBadRequest, "yaml required")
		return
	}

	tmp, cleanup, err := writeTempYAML(body.YAML)
	if err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer cleanup()

	sess := s.readSession(r)
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

	token := s.pty.Issue(ptyConfirmEntry{
		Verb:     "apply",
		Resource: "manifest",
		Argv:     args,
	})

	s.render.JSON(w, http.StatusOK, map[string]any{
		"status":        "needs_confirmation",
		"diff":          buf.String(),
		"diff_html":     diffToHTML(buf.String()),
		"confirm_token": token,
		"ttl":           30,
	})
}

// apiApplyCommit consumes the token from apiApplyPreview and runs the real
// apply. We re-write the YAML to a fresh tmp file because /tmp on the host
// may have been GC'd between the two calls.
func (s *Server) apiApplyCommit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML  string `json:"yaml"`
		Token string `json:"confirm_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.render.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if _, ok := s.pty.Consume(body.Token, ""); !ok {
		s.render.Error(w, http.StatusGone, "confirmation expired or invalid")
		return
	}

	tmp, cleanup, err := writeTempYAML(body.YAML)
	if err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer cleanup()

	sess := s.readSession(r)
	args := sess.prependGlobalFlags([]string{"apply", "-f", tmp})

	var out bytes.Buffer
	start := time.Now()
	runErr := s.deps.Runner.Run(r.Context(), args, kubectl.RunOpts{Stdout: &out, Stderr: &out})
	exitCode := 0
	if runErr != nil {
		exitCode = exitCodeOf(runErr)
	}
	s.recordWebAudit("apply", []string{"-f", "<inline>"}, exitCode, time.Since(start))

	if runErr != nil && exitCode != 0 {
		s.render.JSON(w, http.StatusBadGateway, map[string]any{
			"status": "error",
			"output": out.String(),
		})
		return
	}
	s.render.JSON(w, http.StatusOK, map[string]any{
		"status": "applied",
		"output": out.String(),
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
