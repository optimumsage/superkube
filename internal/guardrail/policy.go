package guardrail

import (
	"regexp"
	"strings"
	"sync"

	"github.com/optimumsage/superkube/internal/config"
)

// Policy is the effective per-context guardrail configuration for the current
// invocation. Empty Policy means "no extra constraints beyond defaults".
type Policy struct {
	// RequireTypedConfirm upgrades any YesNo confirmation in this context to a
	// typed-phrase confirmation. Surfaced via UpgradesYesNo for callers that
	// have already constructed a YesNo prompt.
	RequireTypedConfirm bool

	// Forbidden is the union of operation patterns banned for this context,
	// e.g. ["delete --all", "drain"]. The IsForbidden() helper does the
	// matching.
	Forbidden []string

	// Banner is a short label shown on a colored banner when commands run
	// against this context. Empty = use the matching pattern as the label.
	Banner string

	// MatchedPattern is the config key that matched (e.g. "prod-*"). Useful
	// for diagnostics and the banner default.
	MatchedPattern string
}

// EffectivePolicy resolves the policy for currentContext from the loaded
// config. Globs are matched left-to-right; ALL matching entries contribute
// (RequireTypedConfirm is OR'd, Forbid lists are unioned). The banner from
// the first match wins.
//
// The glob syntax supports `*` (any chars, including `/`) and `?` (one char).
// We deliberately don't use filepath.Match because kubectl context names
// frequently contain `/` (AWS ARN-style names) and we want `prod-*` and
// `arn:*:cluster/prod-*` to both work as users expect.
func EffectivePolicy(cfg *config.Config, currentContext string) Policy {
	p := Policy{RequireTypedConfirm: cfg.Guardrails.RequireTypedConfirm}
	if currentContext == "" || len(cfg.Contexts) == 0 {
		return p
	}
	for pattern, section := range cfg.Contexts {
		if !matchGlob(pattern, currentContext) {
			continue
		}
		if section.RequireTypedConfirm {
			p.RequireTypedConfirm = true
		}
		p.Forbidden = append(p.Forbidden, section.Forbid...)
		if p.MatchedPattern == "" {
			p.MatchedPattern = pattern
		}
		if p.Banner == "" {
			p.Banner = section.Banner
		}
	}
	return p
}

// matchGlob converts a simple glob (with `*` and `?`) to a regex and matches
// it anchored to the full string. Compiled regexes are cached per pattern.
func matchGlob(pattern, s string) bool {
	re, err := globRE(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

var (
	globCache   = map[string]*regexp.Regexp{}
	globCacheMu sync.Mutex
)

func globRE(pattern string) (*regexp.Regexp, error) {
	globCacheMu.Lock()
	defer globCacheMu.Unlock()
	if re, ok := globCache[pattern]; ok {
		return re, nil
	}
	var b strings.Builder
	b.WriteByte('^')
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteByte('$')
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, err
	}
	globCache[pattern] = re
	return re, nil
}

// IsForbidden reports whether an operation described by verb plus args is
// banned by p. Currently we do simple substring matching: a forbid pattern
// like "delete --all" matches if the verb starts with "delete" and the args
// contain "--all". Easy to extend later.
func (p Policy) IsForbidden(verb string, args []string) (rule string, blocked bool) {
	for _, rule := range p.Forbidden {
		tokens := strings.Fields(rule)
		if len(tokens) == 0 {
			continue
		}
		if tokens[0] != verb {
			continue
		}
		allFound := true
		for _, tok := range tokens[1:] {
			if !contains(args, tok) {
				allFound = false
				break
			}
		}
		if allFound {
			return rule, true
		}
	}
	return "", false
}

// UpgradeStyle returns the confirmation style that the policy mandates. If
// the policy says require-typed-confirm, every YesNo becomes a typed phrase;
// otherwise the original style passes through.
func (p Policy) UpgradeStyle(original string) string {
	if !p.RequireTypedConfirm {
		return original
	}
	if original == "yesno" {
		return "typed-phrase"
	}
	return original
}

func contains(args []string, token string) bool {
	for _, a := range args {
		if a == token {
			return true
		}
		// Match flag=value tokens against bare flag names too (`--all` matches
		// `--all=true`).
		if strings.HasPrefix(a, token+"=") {
			return true
		}
	}
	return false
}
