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

func TestFlagsAtPflagSemantics(t *testing.T) {
	// NoOptDefVal flags never consume the next token.
	for _, a := range []string{"--dry-run", "--cascade"} {
		f, _ := LastFlagAt([]string{a, "orphan"}, 0)
		if f.UsesNext {
			t.Errorf("bare %s must not consume the next token", a)
		}
	}
	if f, _ := LastFlagAt([]string{"-context=prod"}, 0); f.Name == "context" {
		t.Error("-context=prod is not a context override in pflag")
	}
	cases := []struct {
		args     []string
		names    []string
		last     string
		usesNext bool
	}{
		{[]string{"-Rf", "dir"}, []string{"recursive", "filename"}, "dir", true},
		{[]string{"-Rfx.yaml"}, []string{"recursive", "filename"}, "x.yaml", false},
		{[]string{"-An", "kube-system"}, []string{"all-namespaces", "namespace"}, "kube-system", true},
		{[]string{"-Ankube-system"}, []string{"all-namespaces", "namespace"}, "kube-system", false},
		{[]string{"-v", "8"}, []string{"v"}, "8", true},
		{[]string{"-v", "get"}, []string{"verbose"}, "", false},
	}
	for _, tc := range cases {
		fs, ok := FlagsAt(tc.args, 0)
		if !ok || len(fs) != len(tc.names) {
			t.Fatalf("%v: %+v", tc.args, fs)
		}
		for i, n := range tc.names {
			if fs[i].Name != n {
				t.Errorf("%v: flag %d = %q, want %q", tc.args, i, fs[i].Name, n)
			}
		}
		l := fs[len(fs)-1]
		if l.UsesNext != tc.usesNext || (tc.last != "" && !tc.usesNext && l.Val != tc.last) {
			t.Errorf("%v: last = %+v", tc.args, l)
		}
	}
}

func TestIsForbiddenClustersAndKindNames(t *testing.T) {
	cases := []struct {
		rule string
		args []string
		want bool
	}{
		{"delete --all-namespaces", []string{"pods", "-A"}, true},
		{"delete -n kube-system", []string{"pods", "-An", "kube-system"}, true},
		{"delete -n kube-system", []string{"pods", "-Ankube-system"}, true},
		{"delete pod/x", []string{"po/x"}, true},
		{"delete pod/x", []string{"pods/x"}, true},
		{"delete pod/x", []string{"pod.v1/x"}, true},
		{"delete pod/x", []string{"pod", "x"}, true},
		{"delete po/x", []string{"pod/x"}, true},
		{"delete pod/x", []string{"pod/y"}, false},
		{"delete pod/x.y", []string{"pod/x.y"}, true},
		{"delete pod/x", []string{"pod", "keep", "--cascade", "orphan"}, false},
		{"delete orphan", []string{"pod", "keep", "--cascade", "orphan"}, true},
	}
	for _, tc := range cases {
		p := Policy{Forbidden: []string{tc.rule}}
		if _, got := p.IsForbidden("delete", tc.args); got != tc.want {
			t.Errorf("rule %q args %v = %v, want %v", tc.rule, tc.args, got, tc.want)
		}
	}
}

func TestEffectivePolicyForKeepsGlobalTypedConfirm(t *testing.T) {
	cfg := &config.Config{Guardrails: config.GuardrailsSection{RequireTypedConfirm: true}}
	if p := EffectivePolicyFor(cfg, nil); !p.RequireTypedConfirm {
		t.Error("global require_typed_confirm dropped with no candidate contexts")
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
