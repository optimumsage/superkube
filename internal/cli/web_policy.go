package cli

import (
	"github.com/optimumsage/superkube/internal/config"
	"github.com/optimumsage/superkube/internal/guardrail"
)

// webPolicyFor resolves the guardrail policy for one kube context. The web UI
// can switch contexts per browser session, so policy is resolved per request
// instead of being frozen at the context `sk web` started in. A config that
// cannot be loaded is an error (the web layer then refuses destructive
// actions) — never "no policy".
func webPolicyFor(kubeContext string) (guardrail.Policy, error) {
	cfg, err := config.Load()
	if err != nil {
		return guardrail.Policy{}, err
	}
	return guardrail.EffectivePolicy(cfg, kubeContext), nil
}

// webBannerFor mirrors showContextBanner for an arbitrary context: an explicit
// policy match wins, otherwise the name-based prod classifier decides. Returns
// ("", "") when nothing applies.
func webBannerFor(kubeContext string) (text, kind string) {
	if cfg, err := config.Load(); err == nil {
		if p := guardrail.EffectivePolicy(cfg, kubeContext); p.MatchedPattern != "" {
			label := p.Banner
			if label == "" {
				label = "context matches " + p.MatchedPattern
			}
			return " ⚠ " + label + " ", "info"
		}
	}
	level := guardrail.ClassifyContext(kubeContext)
	if level == guardrail.RiskNone {
		return "", ""
	}
	text = " ⚠ " + guardrail.AutoBannerLabel(level, kubeContext) + " "
	if level == guardrail.RiskCritical {
		return text, "danger"
	}
	return text, "warn"
}
