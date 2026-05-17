package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/optimumsage/superkube/internal/kubectl"
)

// apiPassthrough runs an arbitrary kubectl command and returns the captured
// output. Used by the command palette (Ctrl+K) so the web UI can match every
// CLI use case, including verbs we haven't built bespoke screens for.
//
// We deliberately reject the destructive subset here — the palette is meant
// for read-only inspection. Destructive verbs route through the typed-confirm
// handlers instead.
func (s *Server) apiPassthrough(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Argv []string `json:"argv"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Argv) == 0 {
		s.render.Error(w, http.StatusBadRequest, "argv required")
		return
	}
	if !palettePassthroughAllowed(body.Argv[0]) {
		s.render.Error(w, http.StatusForbidden,
			"use the dedicated page for destructive verbs ("+body.Argv[0]+")")
		return
	}
	sess := s.readSession(r)
	args := body.Argv
	if !hasFlagN(args) {
		args = sess.prependGlobalFlags(args)
	} else {
		args = sess.prependGlobalFlagsNoNS(args)
	}
	var stdout, stderr bytes.Buffer
	start := time.Now()
	err := s.deps.Runner.Run(r.Context(), args, kubectl.RunOpts{Stdout: &stdout, Stderr: &stderr})
	code := 0
	if err != nil {
		code = exitCodeOf(err)
	}
	s.recordWebAudit("passthrough", body.Argv, code, time.Since(start))
	s.render.JSON(w, http.StatusOK, map[string]any{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": code,
	})
}

// palettePassthroughAllowed gatekeeps which verbs the command palette will
// forward. Read-only verbs only — destructive ones must use a real screen.
func palettePassthroughAllowed(verb string) bool {
	switch verb {
	case "get", "describe", "explain", "events", "top", "config", "api-resources",
		"api-versions", "version", "cluster-info", "auth", "diff", "wait":
		return true
	}
	return false
}

// apiUpgradeCheck reports the current version. A real release check would
// reach GitHub; we leave that path for the CLI's `sk upgrade --check` and
// just expose the local version here so the settings page can render it.
func (s *Server) apiUpgradeCheck(w http.ResponseWriter, _ *http.Request) {
	s.render.JSON(w, http.StatusOK, map[string]any{
		"current_version": currentVersion(),
		"note":            "Run `sk upgrade --check` in a terminal for a live release lookup.",
	})
}
