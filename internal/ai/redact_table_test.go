package ai

import (
	"strings"
	"testing"
)

func TestRedact_Table(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		leaked string // must not appear in output
		keep   string // must appear in output ("" = skip)
	}{
		{"dotted tls.key", "data:\n  tls.key: LS0tTEVBS0VE\n  tls.crt: public", "LS0tTEVBS0VE", "tls.key:"},
		{"dockerconfigjson", "data:\n  .dockerconfigjson: eyJhdXRocyI6e319Zg==", "eyJhdXRocyI6e319Zg==", ".dockerconfigjson"},
		{"json password", `{"level":"info","password":"hunter2","user":"bob"}`, "hunter2", `"user":"bob"`},
		{"json apiKey spaced", `{"apiKey": "abc123XYZ", "x": 1}`, "abc123XYZ", `"x": 1`},
		{"json numeric secret", `{"secret": 12345, "ok": true}`, "12345", `"ok": true`},
		{"midline password=", "2024 connect failed host=db password=hunter2 retry=3", "hunter2", "retry=3"},
		{"midline token=", "GET /api?token=abcdef&page=2", "abcdef", "page=2"},
		{"podspec env name/value", "env:\n- name: DB_PASSWORD\n  value: s3cr3tpw\n- name: LOG_LEVEL\n  value: debug", "s3cr3tpw", "value: debug"},
		{"podspec env json", `{"name":"API_TOKEN","value":"tok123"},{"name":"MODE","value":"fast"}`, "tok123", `"value":"fast"`},
		{"url userinfo", "dsn: postgres://admin:pw0rd@db.local:5432/app", "pw0rd", "admin"},
		{"url userinfo keeps host", "see https://user:p4ss@example.com/x", "p4ss", "example.com"},
		{"pem block", "x\n-----BEGIN RSA PRIVATE KEY-----\nMIIEabc\ndefGHI\n-----END RSA PRIVATE KEY-----\ny", "MIIEabc", "y"},
		{"pem plain", "-----BEGIN PRIVATE KEY-----\nMIIEzzz\n-----END PRIVATE KEY-----", "MIIEzzz", ""},
		{"pem truncated", "-----BEGIN EC PRIVATE KEY-----\nMHcCAQEE\nmore", "MHcCAQEE", ""},
		{"aws akia", "key id AKIAIOSFODNN7EXAMPLE used", "AKIAIOSFODNN7EXAMPLE", "used"},
		{"aws asia", "ASIAIOSFODNN7EXAMPLE", "ASIAIOSFODNN7EXAMPLE", ""},
		{"ini secret", "[default]\naws_access_key_id = AKIAIOSFODNN7EXAMPLE\naws_secret_access_key = wJalrXUtnFEMI/K7MDENG\nregion = us-east-1", "wJalrXUtnFEMI", "region = us-east-1"},
		{"export", "export API_TOKEN=abc123\nexport HOME=/root", "abc123", "HOME=/root"},
	}
	for _, tc := range cases {
		got := Redact(tc.in)
		if strings.Contains(got, tc.leaked) {
			t.Errorf("%s: leaked %q in %q", tc.name, tc.leaked, got)
		}
		if tc.keep != "" && !strings.Contains(got, tc.keep) {
			t.Errorf("%s: over-redacted, missing %q in %q", tc.name, tc.keep, got)
		}
	}
}

func TestRedact_NoFalsePositives(t *testing.T) {
	for _, in := range []string{
		"name: web\nimage: nginx:1.25\nreplicas: 3",
		"PATH=/usr/bin\nHOME=/root",
		"- name: LOG_LEVEL\n  value: debug",
		"https://example.com/path?page=2",
	} {
		if got := Redact(in); got != in {
			t.Errorf("unexpected change:\n in: %q\nout: %q", in, got)
		}
	}
}
