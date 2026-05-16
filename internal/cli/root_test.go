package cli

import "testing"

func TestFirstVerb(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
		ok   bool
	}{
		{"no args", []string{}, "", false},
		{"just verb", []string{"get"}, "get", true},
		{"bool flag then verb", []string{"--yes", "delete"}, "delete", true},
		{"value flag then verb", []string{"--context", "prod", "get"}, "get", true},
		{"value=eq form", []string{"--context=prod", "get"}, "get", true},
		{"short value flag", []string{"-n", "kube-system", "get"}, "get", true},
		{"unknown flag treated as bool", []string{"--unknown", "get"}, "get", true},
		{"two consecutive value flags", []string{"--context", "prod", "-n", "ns", "get"}, "get", true},
		{"only flags", []string{"--yes", "--plain"}, "", false},
		{"-- separator", []string{"--", "weird-verb"}, "weird-verb", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := firstVerb(tc.args)
			if got != tc.want || ok != tc.ok {
				t.Errorf("firstVerb(%v) = (%q, %v), want (%q, %v)", tc.args, got, ok, tc.want, tc.ok)
			}
		})
	}
}
