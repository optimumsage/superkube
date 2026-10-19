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
	if strings.HasPrefix(body.Argv[0], "-") {
		s.render.Error(w, http.StatusBadRequest,
			"put the verb first: flags before the verb (e.g. --context) are not accepted; use the context switcher instead")
		return
	}
	if flag := paletteRejectedFlag(body.Argv); flag != "" {
		s.render.Error(w, http.StatusBadRequest,
			"flag "+flag+" is not allowed in the palette (connection overrides and streaming are rejected)")
		return
	}
	if !palettePassthroughAllowed(body.Argv) {
		s.render.Error(w, http.StatusForbidden,
			"use the dedicated page for destructive verbs ("+strings.Join(body.Argv[:min(len(body.Argv), 2)], " ")+")")
		return
	}
	sess := s.readSession(r)
	// Policy applies to the palette exactly as to the dedicated pages, so a
	// `forbid: ["get secret"]` rule cannot be sidestepped from Ctrl+K.
	// Evaluated on the effective args, i.e. including the session's -n.
	gateArgs := body.Argv[1:]
	if !hasFlagN(body.Argv) && sess.Namespace != "" {
		gateArgs = append(append([]string{}, gateArgs...), "-n", sess.Namespace)
	}
	if s.gateForbidden(w, sess, body.Argv[0], gateArgs) {
		return
	}
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

// paletteConnFlags override which cluster / identity kubectl talks to. The
// palette always runs against the session's pinned context, so none of them
// may be supplied by the request.
var paletteConnFlags = []string{
	"--kubeconfig", "--context", "--server", "--token", "--user", "--cluster",
	"--as", "--as-group", "--as-uid", "--certificate-authority", "--client-key",
	"--client-certificate", "--insecure-skip-tls-verify", "--tls-server-name",
	"--username", "--password", "--proxy-url", "--cache-dir", "--token-file",
}

// paletteStreamFlags would turn a one-shot read into a long-lived stream, or
// make the server read a server-local file / directory on the browser's
// behalf (-f/--filename, -k/--kustomize), neither of which the palette allows.
var paletteStreamFlags = []string{
	"--watch", "--watch-only", "--follow", "--output-watch-events",
	"--filename", "--kustomize",
}

// shortValueFlags take a value, so the rest of a glued short cluster is data
// (`-lapp=web`, `-ndefault`) and must not be scanned for f/w/k.
const shortValueFlags = "lnosctv"

// paletteRejectedFlag returns the first offending flag in argv[1:], or "".
// Everything after a bare "--" is positional data and is not inspected.
func paletteRejectedFlag(argv []string) string {
	for _, a := range argv[1:] {
		if a == "--" {
			return ""
		}
		if !strings.HasPrefix(a, "-") {
			continue
		}
		for _, f := range paletteConnFlags {
			if flagIs(a, f) {
				return f
			}
		}
		// -s (--server) shorthand, including glued forms like -shttps://x.
		if !strings.HasPrefix(a, "--") && strings.HasPrefix(a, "-s") {
			return "-s"
		}
		for _, f := range paletteStreamFlags {
			if flagIs(a, f) {
				return f
			}
		}
		if !strings.HasPrefix(a, "--") {
			// Short cluster: -f/path, -wX, -kdir, -Afw ...
			for _, c := range a[1:] {
				if c == 'f' || c == 'w' || c == 'k' {
					return "-" + string(c)
				}
				if strings.ContainsRune(shortValueFlags, c) {
					break
				}
			}
		}
	}
	return ""
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
