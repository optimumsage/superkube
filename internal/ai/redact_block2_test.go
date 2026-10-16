package ai

import (
	"strings"
	"testing"
)

func TestRedact_BlockScalarsRound2(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		leaked []string
		keep   string
	}{
		{"trailing comment", "  password: | # app cred\n    cmtsecret1\n    cmtsecret2\n  next: ok\n", []string{"cmtsecret1", "cmtsecret2"}, "next: ok"},
		{"double-quoted key", "data:\n  \"tls.key\": |\n    dqsecret\n  next: ok\n", []string{"dqsecret"}, "next: ok"},
		{"single-quoted key", "data:\n  'password': |-\n    sqsecret\n  next: ok\n", []string{"sqsecret"}, "next: ok"},
		{"compact seq item", "items:\n- password: |\n    seqsecret\n  other: ok\n- name: b\n", []string{"seqsecret"}, "other: ok"},
		{"value before name", "env:\n- value: |\n    revsecret1\n    revsecret2\n  name: DB_PASSWORD\n- name: LOG\n  value: debug\n", []string{"revsecret1", "revsecret2"}, "value: debug"},
		{"value before name non-secret kept", "env:\n- value: |\n    plain body\n  name: LOG_LEVEL\n", nil, "plain body"},
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
