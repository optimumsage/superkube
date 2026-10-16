package cli

import (
	"reflect"
	"testing"
)

func TestFirstVerbKubectlValueFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
		idx  int
	}{
		{"--as value", []string{"--as", "admin", "--context", "prod", "delete", "pod", "x"}, "delete", 4},
		{"--token value", []string{"--token", "abc", "get", "pods"}, "get", 2},
		{"--server short", []string{"-s", "https://x", "get"}, "get", 2},
		{"--user", []string{"--user", "bob", "apply"}, "apply", 2},
		{"--request-timeout", []string{"--request-timeout", "5s", "get"}, "get", 2},
		{"--ai-timeout is sk's", []string{"--ai-timeout", "2m", "diagnose"}, "diagnose", 2},
		{"--v with value", []string{"--v", "8", "get"}, "get", 2},
		{"--v=eq", []string{"--v=8", "get"}, "get", 1},
		{"-v numeric is kubectl verbosity", []string{"-v", "8", "get"}, "get", 2},
		{"-v alone is sk --verbose", []string{"-v", "get"}, "get", 1},
		{"bool kubectl flag", []string{"--insecure-skip-tls-verify", "get"}, "get", 1},
		{"match-server-version is bool", []string{"--match-server-version", "get"}, "get", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, i, ok := firstVerbAt(tc.args)
			if !ok || v != tc.want || i != tc.idx {
				t.Errorf("firstVerbAt(%v) = (%q,%d,%v), want (%q,%d,true)", tc.args, v, i, ok, tc.want, tc.idx)
			}
		})
	}
}

func TestScanGlobalFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want scannedFlags
	}{
		{"before verb", []string{"--context", "prod", "delete", "pod", "x"}, scannedFlags{Context: "prod"}},
		{"eq before verb", []string{"--context=prod", "-n=web", "get"}, scannedFlags{Context: "prod", Namespace: "web"}},
		{"after verb", []string{"delete", "pod", "x", "--context", "prod", "-n", "web", "--kubeconfig", "/k"},
			scannedFlags{Context: "prod", Namespace: "web", Kubeconfig: "/k"}},
		{"long namespace", []string{"get", "pods", "--namespace=kube-system"}, scannedFlags{Namespace: "kube-system"}},
		{"stops at --", []string{"exec", "p", "--", "--context", "evil", "-n", "x"}, scannedFlags{}},
		{"value flag value not misread", []string{"--as", "-n", "get"}, scannedFlags{}},
		{"audit", []string{"--audit", "off", "get"}, scannedFlags{Audit: "off"}},
		{"last wins", []string{"--context", "a", "get", "--context", "b"}, scannedFlags{Context: "b"}},
		{"none", []string{"get", "pods"}, scannedFlags{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scanGlobalFlags(tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("scanGlobalFlags(%v) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}
