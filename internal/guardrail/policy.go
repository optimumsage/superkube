package guardrail

import (
	"regexp"
	"sort"
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
// config. ALL matching entries contribute (RequireTypedConfirm is OR'd, Forbid
// lists are unioned). Matches are visited most-specific first (exact name,
// then the glob with the most literal characters, then alphabetical) so the
// reported MatchedPattern and the banner (first non-empty wins) are
// deterministic; Go map iteration order is random and must never leak into
// what the user sees.
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
	var matched []string
	for pattern := range cfg.Contexts {
		if matchGlob(pattern, currentContext) {
			matched = append(matched, pattern)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		si, sj := specificity(matched[i]), specificity(matched[j])
		if si != sj {
			return si > sj
		}
		return matched[i] < matched[j]
	})
	for _, pattern := range matched {
		section := cfg.Contexts[pattern]
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

// EffectivePolicyFor resolves policy for several candidate contexts at once,
// failing closed: forbid rules are the union over all candidates and
// require_typed_confirm is OR'd. contexts[0] is the best guess and supplies
// MatchedPattern/Banner (falling back to the first candidate that has one).
// Used when argv scanning can't prove which context kubectl will really use.
func EffectivePolicyFor(cfg *config.Config, contexts []string) Policy {
	// Start from the context-independent policy so the global
	// guardrails.require_typed_confirm survives even with no candidates.
	out := EffectivePolicy(cfg, "")
	for _, c := range contexts {
		p := EffectivePolicy(cfg, c)
		out.RequireTypedConfirm = out.RequireTypedConfirm || p.RequireTypedConfirm
		out.Forbidden = append(out.Forbidden, p.Forbidden...)
		if out.MatchedPattern == "" {
			out.MatchedPattern = p.MatchedPattern
			if out.Banner == "" {
				out.Banner = p.Banner
			}
		}
	}
	return out
}

// specificity ranks a glob: patterns without wildcards beat any glob; among
// globs, more literal characters means more specific.
func specificity(pattern string) int {
	lit := len(strings.NewReplacer("*", "", "?", "").Replace(pattern))
	if !strings.ContainsAny(pattern, "*?") {
		return 1<<20 + lit
	}
	return lit
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
// banned by p. A rule is a verb followed by tokens that must all appear among
// the args: "delete --all", "drain", "delete namespace". Resource tokens are
// normalized on both sides so "delete namespace" also blocks `delete ns foo`,
// `delete namespaces foo` and `delete namespace/foo`; flags match bare or as
// --flag=value.
func (p Policy) IsForbidden(verb string, args []string) (rule string, blocked bool) {
	have := argTokens(verb, args)
	for _, rule := range p.Forbidden {
		tokens := strings.Fields(rule)
		if len(tokens) == 0 {
			continue
		}
		if tokens[0] != verb {
			continue
		}
		allFound := true
		for _, req := range ruleTokens(verb, tokens[1:]) {
			if !have.has(req) {
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

// token is one comparable unit of an argv or a forbid rule. Flags compare by
// canonical long name, so `-n x`, `-n=x`, `-nx`, `-An x` and `--namespace x`
// all give the same association, and `-A` equals `--all-namespaces`.
type token struct {
	word   string // set for positionals (verbatim)
	norm   string // kind-normalized alternative for type-shaped positionals
	flag   string // canonical flag name; empty for positionals
	val    string
	hasVal bool
}

func ruleTokens(verb string, fields []string) []token {
	var out []token
	typeSeen := false
	nameVerb := nameVerbs[verb]
	for i := 0; i < len(fields); i++ {
		fs, ok := FlagsAt(fields, i)
		if !ok {
			w := fields[i]
			t := token{word: w}
			switch {
			case nameVerb:
				// Positionals are NAMES: verbatim only.
			case strings.Contains(w, "/"):
				// TYPE/NAME in any position: normalize the kind part.
				typeSeen = true
				t.norm = normalizeTypeWord(w)
			case !typeSeen && !rolloutSubs[w]:
				// The first positional (past a rollout-style subcommand) is
				// the resource TYPE slot; later ones are NAMES.
				typeSeen = true
				t.norm = normalizeTypeWord(w)
			}
			out = append(out, t)
			continue
		}
		for j, f := range fs {
			t := token{flag: f.Name, val: f.Val, hasVal: f.HasVal}
			if j == len(fs)-1 && f.UsesNext {
				t.val, t.hasVal = fields[i+1], true
			}
			out = append(out, t)
		}
		if fs[len(fs)-1].UsesNext {
			i++
		}
	}
	return out
}

// nameVerbs take NAMES (or non-resource words) as positionals, never a
// resource TYPE, so their positionals are never kind-normalized.
var nameVerbs = map[string]bool{
	"drain": true, "cordon": true, "uncordon": true,
	"config": true, "version": true, "cluster-info": true,
	"api-resources": true, "api-versions": true, "completion": true,
	"plugin": true, "kustomize": true,
}

type tokenSet struct {
	flags map[string]bool // flag present, any value
	assoc map[string]bool // "flag=value" associations
	words map[string]bool // verbatim positionals, names, and raw types
	norm  map[string]bool // kind-normalized types and TYPE/NAME forms
}

func (t tokenSet) has(req token) bool {
	if req.flag != "" {
		if req.hasVal {
			return t.assoc[req.flag+"="+req.val]
		}
		return t.flags[req.flag]
	}
	return t.words[req.word] || (req.norm != "" && t.norm[req.norm])
}

// normalizeTypeWord canonicalizes a resource-TYPE position: each
// comma-separated type, or the part before `/` in TYPE/NAME, has its
// .group[/version] suffix stripped and is singularized/aliased
// (`pods.v1/x.y` -> `pod/x.y`). NAMES keep their dots and case.
func normalizeTypeWord(w string) string {
	if kind, name, ok := strings.Cut(w, "/"); ok {
		return normalizeKind(kind) + "/" + name
	}
	parts := strings.Split(w, ",")
	for i, p := range parts {
		parts[i] = normalizeKind(p)
	}
	return strings.Join(parts, ",")
}

// argTokens indexes args for rule matching. Values of value-taking flags are
// recorded only as flag=value associations, never as words, so
// `--field-manager namespace` cannot trip a "delete namespace" rule. Unknown
// flags are booleans, so whatever follows stays a word (fail-closed). `--dry-run`
// and `--cascade` never take the next token. Everything after a bare `--` is
// positional. `TYPE a b` also yields TYPE/a and TYPE/b so a `kind/name` rule
// matches either spelling.
func argTokens(verb string, args []string) tokenSet {
	t := tokenSet{flags: map[string]bool{}, assoc: map[string]bool{}, words: map[string]bool{}, norm: map[string]bool{}}
	var positionals []string
	addWord := func(a string) { positionals = append(positionals, a) }
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			for _, w := range args[i+1:] {
				addWord(w)
			}
			break
		}
		fs, ok := FlagsAt(args, i)
		if !ok {
			addWord(args[i])
			continue
		}
		for j, f := range fs {
			t.flags[f.Name] = true
			val, has := f.Val, f.HasVal
			if j == len(fs)-1 && f.UsesNext {
				val, has = args[i+1], true
			}
			if has {
				t.assoc[f.Name+"="+val] = true
			}
		}
		if fs[len(fs)-1].UsesNext {
			i++
		}
	}
	// Every positional is a comparable word verbatim. For resource verbs the
	// first positional (past the verb and a rollout-style subcommand) is a
	// TYPE (possibly a comma list) and the rest are NAMES (possibly comma
	// lists); any TYPE/NAME has its kind normalized. Names never normalize.
	for _, a := range positionals {
		t.words[a] = true
	}
	if nameVerbs[verb] {
		return t
	}
	for len(positionals) > 0 && (positionals[0] == verb || rolloutSubs[positionals[0]]) {
		positionals = positionals[1:]
	}
	var kinds []string
	if len(positionals) > 0 && !strings.Contains(positionals[0], "/") {
		for _, part := range strings.Split(positionals[0], ",") {
			t.words[part] = true
			t.norm[normalizeKind(part)] = true
			kinds = append(kinds, part)
		}
		positionals = positionals[1:]
	}
	for _, a := range positionals {
		if kind, name, ok := strings.Cut(a, "/"); ok {
			t.words[kind] = true
			t.words[name] = true
			t.norm[normalizeKind(kind)] = true
			t.norm[normalizeKind(kind)+"/"+name] = true
			continue
		}
		for _, name := range strings.Split(a, ",") {
			t.words[name] = true
			for _, kind := range kinds {
				t.norm[normalizeKind(kind)+"/"+name] = true
			}
		}
	}
	return t
}

// kindAliases maps kubectl short names and plurals to the singular kind.
var kindAliases = map[string]string{
	"ns": "namespace", "namespaces": "namespace",
	"po": "pod", "pods": "pod",
	"deploy": "deployment", "deployments": "deployment",
	"svc": "service", "services": "service",
	"sts": "statefulset", "statefulsets": "statefulset",
	"ds": "daemonset", "daemonsets": "daemonset",
	"rs": "replicaset", "replicasets": "replicaset",
	"cm": "configmap", "configmaps": "configmap",
	"secrets": "secret",
	"pvc":     "persistentvolumeclaim", "persistentvolumeclaims": "persistentvolumeclaim",
	"pv": "persistentvolume", "persistentvolumes": "persistentvolume",
	"no": "node", "nodes": "node",
	"ing": "ingress", "ingresses": "ingress",
	"sa": "serviceaccount", "serviceaccounts": "serviceaccount",
	"crd": "customresourcedefinition", "crds": "customresourcedefinition",
	"customresourcedefinitions": "customresourcedefinition",
	"hpa":                       "horizontalpodautoscaler", "horizontalpodautoscalers": "horizontalpodautoscaler",
	"jobs": "job",
	"cj":   "cronjob", "cronjobs": "cronjob",
	"netpol": "networkpolicy", "networkpolicies": "networkpolicy",
	"pdb": "poddisruptionbudget", "poddisruptionbudgets": "poddisruptionbudget",
	"sc": "storageclass", "storageclasses": "storageclass",
	"ev": "event", "events": "event",
	"roles": "role", "rolebindings": "rolebinding",
	"clusterroles": "clusterrole", "clusterrolebindings": "clusterrolebinding",
}

// rolloutSubs are `kubectl rollout` subcommands, skipped when synthesizing
// TYPE/NAME from positionals.
var rolloutSubs = map[string]bool{
	"restart": true, "undo": true, "pause": true, "resume": true,
	"status": true, "history": true,
}

// kindSet holds every name normalizeKind recognizes as a kubernetes kind
// (alias keys and their singular targets).
var kindSet = func() map[string]bool {
	m := map[string]bool{}
	for k, v := range kindAliases {
		m[k] = true
		m[v] = true
	}
	return m
}()

// normalizeKind maps a resource TYPE to a canonical comparable form:
// lowercased, any .group[/version] suffix dropped, known aliases/plurals
// resolved, and unknown kinds (CRDs, leases, endpointslices) naively
// singularized. Only call it on TYPE positions, never on names.
func normalizeKind(k string) string {
	l := strings.ToLower(k)
	if base, _, ok := strings.Cut(l, "."); ok && base != "" {
		l = base
	}
	if v, ok := kindAliases[l]; ok {
		return v
	}
	if kindSet[l] || l == "" {
		return l
	}
	switch {
	case strings.HasSuffix(l, "ies"):
		return l[:len(l)-3] + "y"
	case strings.HasSuffix(l, "s") && !strings.HasSuffix(l, "ss"):
		return l[:len(l)-1]
	}
	return l
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
