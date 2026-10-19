package guardrail

import (
	"testing"

	"github.com/optimumsage/superkube/internal/config"
)

func TestIsForbiddenResourceAliases(t *testing.T) {
	p := Policy{Forbidden: []string{"delete namespace", "delete deployment", "delete --all"}}
	cases := []struct {
		verb string
		args []string
		want bool
	}{
		{"delete", []string{"ns", "foo"}, true},
		{"delete", []string{"namespace", "foo"}, true},
		{"delete", []string{"namespaces", "foo"}, true},
		{"delete", []string{"namespace/foo"}, true},
		{"delete", []string{"ns/foo"}, true},
		{"delete", []string{"deploy", "x"}, true},
		{"delete", []string{"deployment.apps/x"}, true},
		{"delete", []string{"deployments.apps", "x"}, true},
		{"delete", []string{"pod", "x"}, false},
		{"delete", []string{"po/x"}, false},
		{"delete", []string{"pods", "--all"}, true},
		{"get", []string{"ns"}, false},
		// A namespace *value* of a flag must not look like a resource kind.
		{"delete", []string{"pod", "x", "-n", "kube-system"}, false},
	}
	for _, tc := range cases {
		_, got := p.IsForbidden(tc.verb, tc.args)
		if got != tc.want {
			t.Errorf("IsForbidden(%q, %v) = %v, want %v", tc.verb, tc.args, got, tc.want)
		}
	}
}

func TestIsForbiddenRuleAliasNormalized(t *testing.T) {
	p := Policy{Forbidden: []string{"delete ns"}}
	if _, ok := p.IsForbidden("delete", []string{"namespace", "foo"}); !ok {
		t.Error("rule written with alias should match full kind")
	}
}

func TestEffectivePolicyDeterministic(t *testing.T) {
	cfg := &config.Config{Contexts: map[string]config.ContextSection{
		"*":         {Banner: "any"},
		"prod-*":    {Banner: "prod glob"},
		"prod-east": {Banner: "exact"},
		"prod-e*":   {Banner: "longer glob"},
	}}
	for i := 0; i < 50; i++ {
		p := EffectivePolicy(cfg, "prod-east")
		if p.MatchedPattern != "prod-east" || p.Banner != "exact" {
			t.Fatalf("iteration %d: got %q/%q, want exact match to win", i, p.MatchedPattern, p.Banner)
		}
	}
	delete(cfg.Contexts, "prod-east")
	for i := 0; i < 50; i++ {
		p := EffectivePolicy(cfg, "prod-east")
		if p.MatchedPattern != "prod-e*" || p.Banner != "longer glob" {
			t.Fatalf("iteration %d: got %q/%q, want longest glob to win", i, p.MatchedPattern, p.Banner)
		}
	}
}
