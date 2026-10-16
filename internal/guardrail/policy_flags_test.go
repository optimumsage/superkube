package guardrail

import (
	"testing"

	"github.com/optimumsage/superkube/internal/config"
)

func TestIsForbiddenExactAndFlagValueTokens(t *testing.T) {
	cases := []struct {
		rule string
		verb string
		args []string
		want bool
	}{
		{"delete --grace-period=0", "delete", []string{"pod", "x", "--grace-period=0"}, true},
		{"delete --grace-period=0", "delete", []string{"pod", "x", "--grace-period", "0"}, true},
		{"delete --grace-period=0", "delete", []string{"pod", "x", "--grace-period=30"}, false},
		{"scale --replicas=0", "scale", []string{"--replicas", "0", "deploy/x"}, true},
		{"scale --replicas=0", "scale", []string{"--replicas=0", "deploy/x"}, true},
		{"scale --replicas=0", "scale", []string{"--replicas=3", "deploy/x"}, false},
		{"scale --replicas 0", "scale", []string{"--replicas=0", "deploy/x"}, true},
		{"delete -n kube-system", "delete", []string{"pod", "x", "-n", "kube-system"}, true},
		{"delete -n kube-system", "delete", []string{"pod", "x", "-n=kube-system"}, true},
		{"delete -n kube-system", "delete", []string{"pod", "x", "-nkube-system"}, true},
		{"delete -n kube-system", "delete", []string{"pod", "x", "--namespace", "kube-system"}, true},
		{"delete --namespace=kube-system", "delete", []string{"pod", "x", "-n", "kube-system"}, true},
		{"delete -n kube-system", "delete", []string{"pod", "x", "-n", "default"}, false},
		{"delete pod/x", "delete", []string{"pod/x"}, true},
		{"delete namespace", "delete", []string{"pod", "x", "--field-manager", "namespace"}, false},
		{"delete namespace", "delete", []string{"--field-manager=namespace", "pod", "x"}, false},
		{"delete --all", "delete", []string{"--all=true", "pods"}, true},
		// An unknown flag is a bool: what follows stays a word (fail-closed).
		{"delete namespace", "delete", []string{"--unknown", "namespace", "x"}, true},
	}
	for _, tc := range cases {
		p := Policy{Forbidden: []string{tc.rule}}
		if _, got := p.IsForbidden(tc.verb, tc.args); got != tc.want {
			t.Errorf("rule %q args %v = %v, want %v", tc.rule, tc.args, got, tc.want)
		}
	}
}

func TestFlagAtDryRunOnlyConsumesValidValue(t *testing.T) {
	f, _ := FlagAt([]string{"--dry-run", "--context=prod"}, 0)
	if f.UsesNext {
		t.Error("bare --dry-run must not swallow --context=prod")
	}
	f, _ = FlagAt([]string{"--dry-run", "NONE"}, 0)
	if !f.UsesNext {
		t.Error("--dry-run NONE (any case) is a value")
	}
	f, _ = FlagAt([]string{"-context=prod"}, 0)
	if f.Name == "context" {
		t.Error("-context=prod is not a context override in pflag")
	}
}

func TestEffectivePolicyForUnion(t *testing.T) {
	cfg := &config.Config{Contexts: map[string]config.ContextSection{
		"prod": {Forbid: []string{"apply"}, RequireTypedConfirm: true, Banner: "P"},
		"dev":  {Banner: "D"},
	}}
	p := EffectivePolicyFor(cfg, []string{"dev", "prod"})
	if _, ok := p.IsForbidden("apply", nil); !ok || !p.RequireTypedConfirm {
		t.Errorf("union lost prod policy: %+v", p)
	}
	if p.Banner != "D" || p.MatchedPattern != "dev" {
		t.Errorf("best guess should drive banner: %+v", p)
	}
}
