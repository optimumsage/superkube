package ai

import (
	"strings"
	"testing"
)

func TestRedact_BlockScalars(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		leaked []string
		keep   string
	}{
		{"literal tls.key", "data:\n  tls.key: |\n    LS0tabc\n    defGHI\n  other: visible\n", []string{"LS0tabc", "defGHI"}, "other: visible"},
		{"strip chomp", "stringData:\n  password: |-\n    line1secret\n    line2secret\nkind: Secret\n", []string{"line1secret", "line2secret"}, "kind: Secret"},
		{"folded", "  api-token: >\n    foldedsecret1\n    foldedsecret2\n  name: web\n", []string{"foldedsecret1", "foldedsecret2"}, "name: web"},
		{"pod env value block", "env:\n- name: DB_PASSWORD\n  value: |\n    multi-secret\n    more-secret\n- name: LOG_LEVEL\n  value: debug\n", []string{"multi-secret", "more-secret"}, "value: debug"},
		{"blank line inside", "  tls.key: |\n    aaa-secret\n\n    bbb-secret\n  next: ok\n", []string{"aaa-secret", "bbb-secret"}, "next: ok"},
	}
	for _, tc := range cases {
		got := Redact(tc.in)
		for _, l := range tc.leaked {
			if strings.Contains(got, l) {
				t.Errorf("%s: leaked %q in %q", tc.name, l, got)
			}
		}
		if !strings.Contains(got, tc.keep) {
			t.Errorf("%s: over-redacted, missing %q in %q", tc.name, tc.keep, got)
		}
	}
}
