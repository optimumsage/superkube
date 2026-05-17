package guardrail

import (
	"regexp"
	"strings"
)

// RiskLevel classifies how dangerous a context is just by its name. It lets us
// surface a banner for `prod-eu-west-1` even when the user hasn't written a
// policy entry for it yet — the cheapest possible safety net.
//
// Heuristic ordering matters: Critical wins over Risky. We rely on word-bound
// matches so "product-staging" doesn't trip the critical path.
type RiskLevel int

const (
	// RiskNone — no automatic concern. Caller may still apply policy.
	RiskNone RiskLevel = iota
	// RiskRisky — name suggests staging-ish env (staging, stage, qa, uat,
	// canary). Warn-styled banner.
	RiskRisky
	// RiskCritical — name suggests a production-class env (prod, production,
	// live, master, main-cluster, mainnet). Danger-styled banner.
	RiskCritical
)

// String returns a stable lowercase label, used in tests and banner copy.
func (r RiskLevel) String() string {
	switch r {
	case RiskCritical:
		return "critical"
	case RiskRisky:
		return "risky"
	default:
		return "none"
	}
}

// Two patterns: a critical set and a risky set. Both anchor on word-boundary-
// like separators (start/end of string, dash, underscore, dot, slash, colon)
// so substrings inside other words don't false-positive. We accept "prod" as
// either a standalone token or the leading/trailing token of a hyphenated
// name — "prod-eu-1", "eu-prod", "aws/prod-cluster" all match.
var (
	criticalRE = regexp.MustCompile(`(?i)(^|[-_./:])(prod|production|live|mainnet|prd)([-_./:]|$)`)
	riskyRE    = regexp.MustCompile(`(?i)(^|[-_./:])(staging|stage|stg|qa|uat|canary|preprod|pre-prod|test)([-_./:]|$)`)
)

// ClassifyContext returns the risk level implied by ctx alone, without consulting
// any config. Empty string → RiskNone.
func ClassifyContext(ctx string) RiskLevel {
	if ctx == "" {
		return RiskNone
	}
	if criticalRE.MatchString(ctx) {
		return RiskCritical
	}
	if riskyRE.MatchString(ctx) {
		return RiskRisky
	}
	return RiskNone
}

// AutoBannerLabel returns a short, human-friendly banner label for an
// auto-classified risk, or "" if RiskNone. The label is intentionally
// uppercased so it stands out from regular log lines.
func AutoBannerLabel(level RiskLevel, ctx string) string {
	switch level {
	case RiskCritical:
		return "PRODUCTION CONTEXT: " + strings.ToUpper(ctx)
	case RiskRisky:
		return "non-prod risky context: " + ctx
	default:
		return ""
	}
}
