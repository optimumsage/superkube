package guardrail

import (
	"testing"

	"github.com/optimumsage/superkube/internal/config"
)

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{"prod-*", "prod-east", true},
		{"prod-*", "staging-east", false},
		{"*tsi-imsar-test*", "arn:aws:eks:us-east-1:111:cluster/tsi-imsar-test-cluster", true},
		{"*tsi-imsar-test*", "arn:aws:eks:us-east-1:111:cluster/dev", false},
		{"arn:*:cluster/prod-*", "arn:aws:eks:us-east-1:111:cluster/prod-east", true},
		{"???", "abc", true},
		{"???", "abcd", false},
		{"plain", "plain", true},
	}
	for _, tc := range cases {
		if got := matchGlob(tc.pattern, tc.input); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.input, got, tc.want)
		}
	}
}

func TestEffectivePolicy_Forbid(t *testing.T) {
	cfg := &config.Config{
		Contexts: map[string]config.ContextSection{
			"prod-*": {Forbid: []string{"delete --all", "drain"}, Banner: "prod!"},
		},
	}
	p := EffectivePolicy(cfg, "prod-east")
	if p.MatchedPattern != "prod-*" {
		t.Errorf("pattern not recorded: %q", p.MatchedPattern)
	}
	if rule, ok := p.IsForbidden("delete", []string{"--all", "pods"}); !ok || rule != "delete --all" {
		t.Errorf("delete --all should be forbidden, got rule=%q ok=%v", rule, ok)
	}
	if _, ok := p.IsForbidden("delete", []string{"pod", "foo"}); ok {
		t.Errorf("regular delete should NOT be forbidden")
	}
	if _, ok := p.IsForbidden("drain", []string{"node-1"}); !ok {
		t.Errorf("drain should be forbidden")
	}
}

func TestEffectivePolicy_NoMatch(t *testing.T) {
	cfg := &config.Config{
		Contexts: map[string]config.ContextSection{
			"prod-*": {Forbid: []string{"drain"}},
		},
	}
	p := EffectivePolicy(cfg, "staging-east")
	if p.MatchedPattern != "" {
		t.Errorf("should not match: %q", p.MatchedPattern)
	}
	if len(p.Forbidden) != 0 {
		t.Errorf("no rules should apply: %v", p.Forbidden)
	}
}
