package guardrail

import "strings"

// This file holds the ONE flag table every argv scanner shares: verb routing,
// global-flag scanning, resource-name extraction (all in internal/cli) and the
// forbid tokenizer. Keeping a single table, parsed the way pflag parses, is
// what stops the scanners from disagreeing about which token is a flag value,
// which is the classic way a guardrail gets bypassed (a value mistaken for the
// verb, or for a resource word).

// Flag is one parsed flag.
type Flag struct {
	// Name is the canonical long name without dashes ("namespace" for -n,
	// "all-namespaces" for -A). Unknown short letters get a "-"-prefixed name
	// that can never collide with a real long flag.
	Name string
	// Val/HasVal hold an attached value (--x=v, -x=v, -xv).
	Val    string
	HasVal bool
	// UsesNext is true when the value is the following token (--x v, -x v).
	UsesNext bool
}

// valueShort maps value-taking shorthands to long names. In pflag a
// single-dash token is a shorthand cluster, never a long flag, so
// `-context=prod` is NOT a context override.
var valueShort = map[byte]string{
	'n': "namespace", 'l': "selector", 'f': "filename", 'k': "kustomize",
	'o': "output", 'c': "container", 's': "server", 'p': "patch",
	'L': "label-columns", 'v': "v",
}

// boolShort maps boolean shorthands to long names, so policy can treat `-A`
// and `--all-namespaces` (or `-R`/`--recursive`) as the same thing.
var boolShort = map[byte]string{
	'A': "all-namespaces", 'R': "recursive", 'w': "watch", 'i': "stdin",
	't': "tty", 'q': "quiet", 'y': "yes", 'h': "help",
}

// valueFlags are canonical long names that take a separate value: superkube's
// own, kubectl's globals (`kubectl options`) and per-verb value flags common
// in guarded commands. `--ai` is deliberately absent: it takes a value only
// as a root flag before the verb (see internal/cli), and is a boolean for
// `logs --ai`. `--dry-run` and `--cascade` are absent too: they have
// NoOptDefVal in kubectl, so pflag never reads the next token as their value.
var valueFlags = map[string]bool{}

// knownBool are long flag names known to take NO value (superkube's and
// kubectl's); used to judge whether a token before a `--context` is a plain
// boolean or an unknown flag that might have swallowed it as its value.
var knownBool = map[string]bool{}

// kubectlGlobal are the flags kubectl accepts before any verb.
var kubectlGlobal = map[string]bool{}

// skRoot are superkube's own root flags.
var skRoot = map[string]bool{}

func init() {
	for _, n := range []string{
		"audit", "ai-timeout",
		"as", "as-group", "as-uid", "cache-dir", "certificate-authority",
		"client-certificate", "client-key", "cluster", "context", "kubeconfig",
		"namespace", "request-timeout", "server", "tls-server-name", "token",
		"user", "username", "password", "log-file", "log-flush-frequency",
		"vmodule", "profile", "profile-output", "v",
		"filename", "kustomize", "selector", "field-selector", "output",
		"container", "replicas", "current-replicas", "resource-version",
		"to-revision", "grace-period", "timeout", "field-manager",
		"pod-selector", "chunk-size", "template", "for", "label-columns",
		"sort-by", "prune-allowlist", "subresource", "patch", "patch-file",
		"type", "image", "port", "target-port", "name", "overrides", "labels",
		"from-file", "from-literal", "from-env-file", "limits", "requests",
		"skip-wait-for-delete-timeout",
		// logs
		"tail", "since", "since-time", "limit-bytes", "max-log-requests",
	} {
		valueFlags[n] = true
	}
	for _, n := range []string{
		"yes", "plain", "verbose", "no-context", "tools", "help", "version",
		"insecure-skip-tls-verify", "match-server-version", "warnings-as-errors",
		"disable-compression", "all", "all-namespaces", "force", "now",
		"recursive", "watch", "watch-only", "ignore-not-found", "wait",
		"overwrite", "server-side", "follow", "previous", "timestamps",
		"stdin", "tty", "quiet", "show-labels", "no-headers", "show-kind",
		"dry-run", "cascade", "prefix", "all-containers", "ai",
	} {
		knownBool[n] = true
	}
	for _, n := range []string{
		"as", "as-group", "as-uid", "cache-dir", "certificate-authority",
		"client-certificate", "client-key", "cluster", "context", "kubeconfig",
		"namespace", "request-timeout", "server", "tls-server-name", "token",
		"user", "username", "password", "log-file", "log-flush-frequency",
		"vmodule", "profile", "profile-output", "v",
		"insecure-skip-tls-verify", "match-server-version",
		"warnings-as-errors", "disable-compression",
	} {
		kubectlGlobal[n] = true
	}
	for _, n := range []string{
		"yes", "plain", "audit", "ai", "ai-timeout", "no-context", "tools",
		"verbose", "dry-run", "help", "version",
	} {
		skRoot[n] = true
	}
}

// IsKubectlGlobal reports whether name is a kubectl-global flag.
func IsKubectlGlobal(name string) bool { return kubectlGlobal[name] }

// IsSkRoot reports whether name is one of superkube's own root flags.
func IsSkRoot(name string) bool { return skRoot[name] }

// IsKnownBool reports whether name is a flag known to take no value.
func IsKnownBool(name string) bool { return knownBool[name] }

// IsNumeric reports whether s is a non-empty run of digits.
func IsNumeric(s string) bool {
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

// FlagsAt parses args[i] the way pflag does and returns every flag the token
// carries (a short cluster like `-Rfx.yaml` is several). ok is false for
// positionals, "-" and "--". Only the last returned flag can have UsesNext.
func FlagsAt(args []string, i int) (fs []Flag, ok bool) {
	a := args[i]
	if len(a) < 2 || a[0] != '-' || a == "--" {
		return nil, false
	}
	if strings.HasPrefix(a, "--") {
		name, val, eq := strings.Cut(a[2:], "=")
		f := Flag{Name: name}
		switch {
		case eq:
			f.Val, f.HasVal = val, true
		case valueFlags[name]:
			f.UsesNext = i+1 < len(args)
		}
		return []Flag{f}, true
	}
	// Short cluster: bool shorthands are consumed one by one; the first
	// value-taking shorthand takes the rest of the token, or the next arg.
	for rest := a[1:]; rest != ""; {
		c := rest[0]
		long, isVal := valueShort[c]
		if !isVal {
			name, known := boolShort[c]
			if !known {
				name = "-" + string(c)
			}
			fs = append(fs, Flag{Name: name})
			rest = rest[1:]
			continue
		}
		tail := rest[1:]
		f := Flag{Name: long}
		switch {
		case tail == "":
			// -v is kubectl verbosity only with a numeric next token;
			// otherwise it is superkube's boolean --verbose.
			if c == 'v' {
				if i+1 < len(args) && IsNumeric(args[i+1]) {
					f.UsesNext = true
				} else {
					f.Name = "verbose"
				}
			} else {
				f.UsesNext = i+1 < len(args)
			}
		case tail[0] == '=':
			f.Val, f.HasVal = tail[1:], true
		case c == 'v' && !IsNumeric(tail):
			fs = append(fs, Flag{Name: "verbose"})
			rest = tail
			continue
		default:
			f.Val, f.HasVal = tail, true
		}
		fs = append(fs, f)
		break
	}
	return fs, true
}

// LastFlagAt is FlagsAt reduced to the flag that decides how many argv tokens
// the token consumes (the value-bearing one, or the last of a cluster).
func LastFlagAt(args []string, i int) (Flag, bool) {
	fs, ok := FlagsAt(args, i)
	if !ok {
		return Flag{}, false
	}
	return fs[len(fs)-1], true
}
