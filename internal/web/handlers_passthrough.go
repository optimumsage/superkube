package web

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/optimumsage/superkube/internal/kubectl"
)

// apiPassthrough runs a read-only kubectl command and returns the captured
// output. Used by the command palette (Ctrl+K) so the web UI can match every
// CLI use case, including verbs we haven't built bespoke screens for.
//
// We deliberately reject everything that can write — the palette is meant for
// read-only inspection. Destructive verbs route through the typed-confirm
// handlers instead.
func (s *Server) apiPassthrough(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Argv []string `json:"argv"`
	}
	if err := s.decodeJSON(w, r, &body); err != nil || len(body.Argv) == 0 {
		s.render.Error(w, http.StatusBadRequest, "argv required")
		return
	}
	if !palettePassthroughAllowed(body.Argv) {
		s.render.Error(w, http.StatusForbidden,
			"use the dedicated page for destructive verbs ("+strings.Join(body.Argv[:min(len(body.Argv), 2)], " ")+")")
		return
	}
	sess := s.readSession(r)
	args := sess.kubectlArgs(body.Argv)
	var stdout, stderr bytes.Buffer
	start := time.Now()
	err := s.deps.Runner.Run(r.Context(), args, kubectl.RunOpts{Stdout: &stdout, Stderr: &stderr})
	code := 0
	if err != nil {
		code = exitCodeOf(err)
	}
	s.recordWebAudit(r, "passthrough", body.Argv, code, time.Since(start))
	s.render.JSON(w, http.StatusOK, map[string]any{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": code,
	})
}

// palettePassthroughAllowed gatekeeps what the command palette will forward:
// read-only invocations only. Sub-commands matter — `config` also contains
// set-credentials / delete-context / use-context, and `auth` contains
// `reconcile`, which writes RBAC objects.
func palettePassthroughAllowed(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch argv[0] {
	case "get", "describe", "explain", "events", "top", "api-resources",
		"api-versions", "version", "cluster-info", "diff", "wait":
		return true
	case "config":
		if len(argv) < 2 {
			return false
		}
		switch argv[1] {
		case "get-contexts", "current-context":
			return true
		case "view":
			// --raw reveals embedded credentials; --flatten inlines cert and
			// key material.
			for _, a := range argv[2:] {
				if flagIs(a, "--raw") || flagIs(a, "--flatten") {
					return false
				}
			}
			return true
		}
		return false
	case "auth":
		return len(argv) >= 2 && (argv[1] == "can-i" || argv[1] == "whoami")
	}
	return false
}

// flagIs reports whether arg is the named long flag, bare or with =value.
func flagIs(arg, name string) bool {
	return arg == name || strings.HasPrefix(arg, name+"=")
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
