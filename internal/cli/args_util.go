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
	out := args[:0]
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

// positionalArgs returns the slice of args that don't look like flags. Used to
// find the kubectl resource type and name(s).
func positionalArgs(args []string) []string {
	var out []string
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(a, "-") {
			// If it's --name=value form, single token. Otherwise, we'd need to
			// know if it's a value-taking flag; we conservatively treat it as
			// one token (so values that happen to be positional like
			// "kubectl delete pod -n default foo" would mis-classify "default"
			// as positional). Not perfect but good enough for v0.1 guardrail
			// heuristics; the actual kubectl call uses the full args verbatim.
			continue
		}
		out = append(out, a)
	}
	return out
}
