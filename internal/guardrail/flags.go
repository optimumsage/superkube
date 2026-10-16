package guardrail

import "strings"

// This file holds the ONE flag table every argv scanner shares: verb routing,
// global-flag scanning, resource-name extraction (all in internal/cli) and the
// forbid tokenizer. Keeping a single table is what stops the scanners from
// disagreeing about which token is a flag value, which is the classic way a
// guardrail gets bypassed (a value mistaken for the verb, or a resource word).

// Flag is one parsed flag token.
type Flag struct {
	// Name is the canonical long name without dashes ("namespace" for -n).
	// Unknown short clusters get a "-"-prefixed name that can never collide
	// with a real long flag.
	Name string
	// Val/HasVal hold an attached value (--x=v, -x=v, -xv).
	Val    string
	HasVal bool
	// UsesNext is true when the value is the following token (--x v, -x v).
	UsesNext bool
}

// shortFlags maps kubectl value-taking shorthands to their long names. In
// pflag a single-dash token is a shorthand cluster, never a long flag, so
// `-context=prod` is NOT a context override.
var shortFlags = map[byte]string{
	'n': "namespace", 'l': "selector", 'f': "filename", 'k': "kustomize",
	'o': "output", 'c': "container", 's': "server", 'p': "patch",
	'L': "label-columns", 'v': "v",
}

// valueFlags are canonical long names of flags that take a separate value:
// superkube's own, kubectl's globals (`kubectl options`) and the per-verb
// value flags common in guarded commands.
var valueFlags = map[string]bool{}

func init() {
	for _, n := range []string{
		// superkube
		"ai", "audit", "ai-timeout",
		// kubectl globals
		"as", "as-group", "as-uid", "cache-dir", "certificate-authority",
		"client-certificate", "client-key", "cluster", "context", "kubeconfig",
		"namespace", "request-timeout", "server", "tls-server-name", "token",
		"user", "username", "password", "log-file", "log-flush-frequency",
		"vmodule", "profile", "profile-output",
		// per-verb
		"filename", "kustomize", "selector", "field-selector", "output",
		"container", "replicas", "current-replicas", "resource-version",
		"to-revision", "grace-period", "timeout", "field-manager",
		"pod-selector", "chunk-size", "template", "for", "label-columns",
		"sort-by", "prune-allowlist", "subresource", "patch", "patch-file",
		"type", "image", "port", "target-port", "name", "overrides", "labels",
		"from-file", "from-literal", "from-env-file", "limits", "requests",
		"skip-wait-for-delete-timeout",
	} {
		valueFlags[n] = true
	}
}

// optionalValue flags only take the NEXT token when it is one of their valid
// values (pflag NoOptDefVal semantics: `--dry-run` alone is legal). Treating
// them as always-value flags would swallow `--context=prod` or a verb.
var optionalValue = map[string]func(string) bool{
	"dry-run": oneOfFold("none", "client", "server", "auto", "unchanged", "true", "false"),
	"cascade": oneOfFold("background", "foreground", "orphan", "true", "false"),
	// kubectl verbosity: `-v 8` / `--v 8`; sk's own -v is a bare boolean.
	"v": IsNumeric,
}

func oneOfFold(vals ...string) func(string) bool {
	return func(s string) bool {
		s = strings.ToLower(s)
		for _, v := range vals {
			if s == v {
				return true
			}
		}
		return false
	}
}

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

// IsDryRunValue reports whether s is a valid --dry-run value (any case).
func IsDryRunValue(s string) bool { return optionalValue["dry-run"](s) }

// FlagAt parses args[i] as a flag. ok is false for positionals, "-" and "--".
func FlagAt(args []string, i int) (f Flag, ok bool) {
	a := args[i]
	if len(a) < 2 || a[0] != '-' || a == "--" {
		return Flag{}, false
	}
	if strings.HasPrefix(a, "--") {
		name, val, eq := strings.Cut(a[2:], "=")
		f.Name = name
		if eq {
			f.Val, f.HasVal = val, true
			return f, true
		}
		f.UsesNext = consumesNext(args, i, name)
		return f, true
	}
	c := a[1]
	canon, known := shortFlags[c]
	if !known {
		f.Name = "-" + a[1:] // bool shorthand cluster
		return f, true
	}
	f.Name = canon
	rest := a[2:]
	switch {
	case rest == "":
		f.UsesNext = consumesNext(args, i, canon)
	case rest[0] == '=':
		f.Val, f.HasVal = rest[1:], true
	case c == 'v' && !IsNumeric(rest):
		f.Name = "-" + a[1:] // e.g. -vv: bool cluster, not verbosity
	default:
		f.Val, f.HasVal = rest, true // attached: -nprod, -fx.yaml, -lapp=x
	}
	return f, true
}

func consumesNext(args []string, i int, name string) bool {
	if i+1 >= len(args) {
		return false
	}
	if valid, has := optionalValue[name]; has {
		return valid(args[i+1])
	}
	return valueFlags[name]
}
