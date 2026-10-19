package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/guardrail"
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

// flagSpan returns how many argv tokens the flag token at args[i] occupies
// (1, or 2 when its value is the next token). ok is false for positionals.
func flagSpan(args []string, i int) (n int, ok bool) {
	_, n, ok = parseTok(args, i, false)
	return n, ok
}

// positionalArgs returns the slice of args that are neither flags nor flag
// values. Used to find the kubectl resource type and name(s). Flag/value
// pairing and short-cluster parsing (`-Rf dir`, `-An ns`) come from the shared
// guardrail.FlagsAt table. Tokens after a bare `--` are all positional.
// Callers must check the length before indexing the result.
func positionalArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); {
		if args[i] == "--" {
			out = append(out, args[i+1:]...)
			break
		}
		n, ok := flagSpan(args, i)
		if !ok {
			out = append(out, args[i])
			n = 1
		}
		i += n
	}
	return out
}

// hasSource reports whether args select resources from a manifest, kustomize
// dir or selector (-f/-k/-l and long forms, including attached `-fx.yaml` and
// clusters like `-Rf dir`) rather than by name.
func hasSource(args []string) bool {
	for i := 0; i < len(args); {
		if args[i] == "--" {
			return false
		}
		fs, ok := guardrail.FlagsAt(args, i)
		if !ok {
			i++
			continue
		}
		for _, f := range fs {
			switch f.Name {
			case "filename", "kustomize", "selector", "field-selector":
				return true
			}
		}
		n, _ := flagSpan(args, i)
		i += n
	}
	return false
}

// splitTargets turns positionals into (type, names). Supports `TYPE NAME...`
// and the all-`TYPE/NAME` form (type is then empty and each name keeps its
// kind prefix). Comma lists are split so the confirmation counts every target
// kubectl will touch: `pod/a,pod/b`, `pod a,b`, and comma kinds
// (`pod,svc app` = pod/app and svc/app, returned with an empty type).
func splitTargets(positionals []string) (resourceType string, names []string) {
	if len(positionals) == 0 {
		return "", nil
	}
	allSlashed := true
	for _, p := range positionals {
		if !strings.Contains(p, "/") {
			allSlashed = false
			break
		}
	}
	rest := positionals
	if !allSlashed {
		resourceType, rest = positionals[0], positionals[1:]
	}
	var flat []string
	for _, p := range rest {
		for _, n := range strings.Split(p, ",") {
			if n != "" {
				flat = append(flat, n)
			}
		}
	}
	if kinds := strings.Split(resourceType, ","); len(kinds) > 1 {
		for _, k := range kinds {
			for _, n := range flat {
				names = append(names, k+"/"+n)
			}
		}
		return "", names
	}
	return resourceType, flat
}

// boolFlag reports whether a boolean flag, given by canonical long name
// ("yes", "all"), is set in args. It uses the same FlagsAt walk as routing,
// scanning and stripping, so a token consumed as another flag's value
// (`--audit --yes`) is never mistaken for the flag, and clusters (`-y`,
// `-yRf x`) and `=false`/`=0` are honored. The last occurrence wins.
func boolFlag(args []string, names ...string) bool {
	set := false
	for i := 0; i < len(args); {
		if args[i] == "--" {
			break
		}
		fs, n, ok := parseTok(args, i, false)
		if !ok {
			i++
			continue
		}
		for j, f := range fs {
			for _, name := range names {
				if f.Name != name {
					continue
				}
				val := f.Val
				if j == len(fs)-1 && f.UsesNext {
					val = args[i+1]
				}
				set = truthy(f, val)
			}
		}
		i += n
	}
	return set
}

// skOnlyBool are superkube flags that take no value and must never reach
// kubectl; skOnlyValue take a value.
var (
	skOnlyBool  = map[string]bool{"yes": true, "plain": true, "no-context": true, "tools": true, "verbose": true}
	skOnlyValue = map[string]bool{"audit": true, "ai-timeout": true}
)

// stripSkFlags removes superkube-only flags so they are never forwarded to
// kubectl (which would reject them): --yes/-y, --plain, --verbose/-v,
// --no-context, --tools, --audit X, --ai-timeout X, in `--x v`, `--x=v` and
// short-cluster forms (`-yRf dir` becomes `-Rf dir`). It walks argv with the
// shared FlagsAt table, so values of other flags are never touched. Every
// DisableFlagParsing command that shells out to kubectl must use it.
func stripSkFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); {
		if args[i] == "--" {
			return append(out, args[i:]...)
		}
		fs, n, ok := parseTok(args, i, false)
		if !ok {
			out = append(out, args[i])
			i++
			continue
		}
		a := args[i]
		if strings.HasPrefix(a, "--") {
			if skOnlyBool[fs[0].Name] || skOnlyValue[fs[0].Name] {
				i += n
				continue
			}
			out = append(out, args[i:i+n]...)
			i += n
			continue
		}
		// Short cluster: drop sk-only boolean letters, keep the rest.
		var letters strings.Builder
		tail := ""
		for _, f := range fs {
			switch {
			case f.Takes:
				tail = a[f.Idx:]
			case skOnlyBool[f.Name]:
			default:
				letters.WriteByte(f.Short)
				if f.HasVal {
					tail = "=" + f.Val
				}
			}
		}
		if letters.Len() == 0 && tail == "" {
			i += n
			continue
		}
		out = append(out, "-"+letters.String()+tail)
		if n == 2 {
			out = append(out, args[i+1])
		}
		i += n
	}
	return out
}

// isDryRunArgs reports whether args carry a NON-mutating kubectl dry-run
// (`--dry-run`, `=client`, `=server`, `=true`). Such runs change nothing, so
// they need no confirmation; `=none`/`=false`/`=auto` still mutate.
func isDryRunArgs(args []string) bool {
	dry := false
	for i := 0; i < len(args); {
		if args[i] == "--" {
			break
		}
		fs, n, ok := parseTok(args, i, false)
		if !ok {
			i++
			continue
		}
		for _, f := range fs {
			if f.Name != "dry-run" {
				continue
			}
			v := "client"
			if f.HasVal {
				v = strings.ToLower(f.Val)
			}
			dry = v == "client" || v == "server" || v == "true" || v == "unchanged"
		}
		i += n
	}
	return dry
}
