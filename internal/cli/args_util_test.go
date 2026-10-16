package cli

import (
	"reflect"
	"testing"
)

func TestPositionalArgsSkipsFlagValues(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"plain", []string{"pod", "x"}, []string{"pod", "x"}},
		{"global flag before verb args", []string{"--context", "prod", "pod", "x"}, []string{"pod", "x"}},
		{"-n between", []string{"undo", "-n", "prod", "deploy/x"}, []string{"undo", "deploy/x"}},
		{"-n=eq", []string{"pod", "-n=prod", "x"}, []string{"pod", "x"}},
		{"-f file only", []string{"-f", "x.yaml"}, nil},
		{"selector", []string{"pods", "-l", "app=x"}, []string{"pods"}},
		{"bool flags don't eat", []string{"--all", "pods", "--force", "x"}, []string{"pods", "x"}},
		{"kubectl -v numeric", []string{"-v", "8", "pod", "x"}, []string{"pod", "x"}},
		{"sk -v bool", []string{"-v", "pod", "x"}, []string{"pod", "x"}},
		{"token value", []string{"--token", "abc", "pod", "x"}, []string{"pod", "x"}},
		{"grace period", []string{"pod", "x", "--grace-period", "0"}, []string{"pod", "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := positionalArgs(tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("positionalArgs(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestStripFlagDoesNotMutateInput(t *testing.T) {
	in := []string{"--yes", "pod", "x", "-y"}
	orig := append([]string(nil), in...)
	out := stripFlag(in, "--yes")
	out = stripFlag(out, "-y")
	if !reflect.DeepEqual(in, orig) {
		t.Errorf("stripFlag mutated its input: %v, want %v", in, orig)
	}
	if !reflect.DeepEqual(out, []string{"pod", "x"}) {
		t.Errorf("out = %v", out)
	}
}
