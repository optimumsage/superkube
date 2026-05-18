package cli

import (
	"reflect"
	"testing"
)

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

func TestFirstVerbAtIndex(t *testing.T) {
	// Pin the index too — the `sk pods` → `sk get pods` rewrite uses it to
	// splice "get" in at the right position. If firstVerbAt's idx ever drifts
	// out of sync with where firstVerb stopped, the rewrite would corrupt argv.
	cases := []struct {
		name string
		args []string
		verb string
		idx  int
		ok   bool
	}{
		{"verb at 0", []string{"pods"}, "pods", 0, true},
		{"after one bool flag", []string{"--yes", "pods"}, "pods", 1, true},
		{"after value flag", []string{"-n", "test", "pods"}, "pods", 2, true},
		{"after equals form", []string{"--context=prod", "pods"}, "pods", 1, true},
		{"after -- separator", []string{"--", "pods"}, "pods", 1, true},
		{"no positional", []string{"--yes"}, "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, i, ok := firstVerbAt(tc.args)
			if v != tc.verb || i != tc.idx || ok != tc.ok {
				t.Errorf("firstVerbAt(%v) = (%q, %d, %v), want (%q, %d, %v)",
					tc.args, v, i, ok, tc.verb, tc.idx, tc.ok)
			}
		})
	}
}

func TestLooksLikeResource(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"pods", true},
		{"po", true},
		{"pod", true},
		{"svc", true},
		{"services", true},
		{"deploy", true},
		{"deployments", true},
		{"ns", true},
		{"all", true},

		// kind/name form
		{"pod/foo", true},
		{"deploy/web", true},

		// comma-joined — all parts must be known
		{"pods,svc", true},
		{"pods,svc,deploy", true},
		{"pods,not-a-resource", false},

		// non-resources
		{"", false},
		{"diagnose", false}, // sk subcommand (not classified here, routing handles it)
		{"weird-krew-plugin", false},
		{"events", false}, // deliberately excluded — kubectl has a top-level `events`
		{"ev", false},

		// trailing slash without a name shouldn't pass
		{"/foo", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := looksLikeResource(tc.in); got != tc.want {
				t.Errorf("looksLikeResource(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestInsertVerbAt(t *testing.T) {
	cases := []struct {
		name string
		args []string
		idx  int
		verb string
		want []string
	}{
		{"insert at 0", []string{"pods", "-n", "test"}, 0, "get",
			[]string{"get", "pods", "-n", "test"}},
		{"insert mid", []string{"-n", "test", "pods"}, 2, "get",
			[]string{"-n", "test", "get", "pods"}},
		{"insert at end", []string{"-n", "test"}, 2, "get",
			[]string{"-n", "test", "get"}},
		{"empty args", []string{}, 0, "get", []string{"get"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := insertVerbAt(tc.args, tc.idx, tc.verb)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("insertVerbAt(%v, %d, %q) = %v, want %v", tc.args, tc.idx, tc.verb, got, tc.want)
			}
		})
	}
}
