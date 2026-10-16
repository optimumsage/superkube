package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// wantsHelp returns true if args contains a help flag in any common form.
// Commands with DisableFlagParsing=true never let cobra intercept --help, so
// they call this and print the help text manually.
func wantsHelp(args []string) bool {
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			return true
		}
	}
	return false
}

// printHelpIfRequested prints cmd's usage to stderr and returns true when the
// args ask for help. Call early in a DisableFlagParsing RunE.
func printHelpIfRequested(cmd *cobra.Command, args []string) bool {
	if !wantsHelp(args) {
		return false
	}
	_ = cmd.Help()
	return true
}

// hasFlag returns true if any token in args equals name or starts with name+"=".
// Used by enhanced commands to detect superkube-level flags (--yes) that the
// user placed after the verb. With DisableFlagParsing on the subcommand,
// cobra won't fill Flags.Yes in that case, so we scan manually.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

// flagValue returns the value of name in args, looking at both --name=value
// and --name value forms. Returns ok=false if absent.
func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == name {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimPrefix(a, name+"="), true
		}
	}
	return "", false
}

// effectiveNamespace returns the namespace this invocation should use,
// consulting (1) the root persistent flag, then (2) inline -n / --namespace
// in args. Used by DisableFlagParsing commands where the persistent flag
// doesn't always bind in time.
func effectiveNamespace(args []string) string {
	if Flags.Namespace != "" {
		return Flags.Namespace
	}
	if v, ok := flagValue(args, "--namespace"); ok {
		return v
	}
	if v, ok := flagValue(args, "-n"); ok {
		return v
	}
	return ""
}

// effectiveContext mirrors effectiveNamespace for --context.
func effectiveContext(args []string) string {
	if Flags.Context != "" {
		return Flags.Context
	}
	if v, ok := flagValue(args, "--context"); ok {
		return v
	}
	return ""
}

// stripFlag removes a flag (and its value, if the value form is --name value)
// from args. Used to ensure superkube-internal flags like --yes don't get
// passed through to kubectl, which would reject them.
func stripFlag(args []string, name string) []string {
	// Fresh slice, never args[:0]: filtering in place would corrupt the
	// caller's argv, which is still read afterwards (policy check, names).
	out := make([]string, 0, len(args))
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if a == name {
			// Value form: skip the next token too. We can't always know if
			// it's a boolean flag or a value-taking one without external info;
			// for the names we use this with (--yes, which is bool), there's
			// no value. Conservatively: don't skip the next token here. Callers
			// that need value-stripping should use stripFlagWithValue.
			continue
		}
		if strings.HasPrefix(a, name+"=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// stripFlagWithValue removes a value-taking flag from args in both forms:
// `--name=value` (one token) and `--name value` (two tokens). Used by
// DisableFlagParsing commands to drop superkube-internal flags like
// --ai-timeout before forwarding the rest to kubectl, which would reject them.
func stripFlagWithValue(args []string, name string) []string {
	out := make([]string, 0, len(args))
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if a == name {
			skipNext = true // drop the following value token too
			continue
		}
		if strings.HasPrefix(a, name+"=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// firstPositional returns the first non-flag token in args, or "" if none.
// Naive: treats "--name=value" as a flag, "-x" as a flag, but does not know
// whether "-x" consumes the next token. For our use cases this is fine.
func firstPositional(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// positionalArgs returns the slice of args that are neither flags nor flag
// values. Used to find the kubectl resource type and name(s). Value-taking
// flags written as `--name value` (kubectl's global flags plus the common
// per-verb ones, see valueFlags) have their value skipped so
// `delete -n prod pod x` yields [pod x], not [prod pod x].
func positionalArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			out = append(out, a)
			continue
		}
		if flagConsumesNext(args, i, valueFlags) {
			i++
		}
	}
	return out
}

// flagConsumesNext reports whether args[i] (a flag token) takes args[i+1] as
// its value according to the given flag-name set. `--name=value` forms never
// do. `-v`/`--v` is ambiguous: kubectl's is a verbosity level (`-v 8`) while
// superkube's own `-v` is the boolean --verbose; we treat the next token as
// the value only when it is numeric, which no verb or resource name ever is.
func flagConsumesNext(args []string, i int, set map[string]bool) bool {
	a := args[i]
	if strings.Contains(a, "=") || i+1 >= len(args) {
		return false
	}
	if a == "-v" || a == "--v" {
		return isNumeric(args[i+1])
	}
	return set[strings.TrimLeft(a, "-")]
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// globalValueFlags are the flags (without leading dashes) that take a
// separate value and are accepted before the verb: superkube's own root flags
// plus every value-taking kubectl global flag (see `kubectl options`).
// Bool globals (--insecure-skip-tls-verify, --match-server-version,
// --warnings-as-errors, --disable-compression) are deliberately absent.
// -v/--v is handled specially in flagConsumesNext.
var globalValueFlags = map[string]bool{
	// superkube
	"ai": true, "dry-run": true, "audit": true, "ai-timeout": true,
	// kubectl
	"as": true, "as-group": true, "as-uid": true, "cache-dir": true,
	"certificate-authority": true, "client-certificate": true, "client-key": true,
	"cluster": true, "context": true, "kubeconfig": true,
	"n": true, "namespace": true, "request-timeout": true,
	"s": true, "server": true, "tls-server-name": true, "token": true,
	"user": true, "username": true, "password": true,
	"log-file": true, "log-flush-frequency": true, "vmodule": true,
	"profile": true, "profile-output": true,
}

// valueFlags extends globalValueFlags with the per-verb flags that commonly
// appear in guarded commands (delete, scale, rollout, drain, apply, ...) and
// take a separate value. Used only to pick resource names out of argv.
var valueFlags = func() map[string]bool {
	m := map[string]bool{}
	for k := range globalValueFlags {
		m[k] = true
	}
	for _, k := range []string{
		"f", "filename", "k", "kustomize", "l", "selector", "field-selector",
		"o", "output", "c", "container", "replicas", "current-replicas",
		"resource-version", "to-revision", "grace-period", "timeout", "cascade",
		"field-manager", "pod-selector", "chunk-size", "template", "for",
		"label-columns", "sort-by", "prune-allowlist", "subresource",
		"skip-wait-for-delete-timeout",
	} {
		m[k] = true
	}
	return m
}()
