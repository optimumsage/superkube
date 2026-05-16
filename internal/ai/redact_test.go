package ai

import (
	"strings"
	"testing"
)

func TestRedact_JWT(t *testing.T) {
	in := "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature"
	got := Redact(in)
	if strings.Contains(got, "eyJ") {
		t.Errorf("JWT not redacted: %q", got)
	}
	if !strings.Contains(got, "<redacted") {
		t.Errorf("expected redaction marker, got: %q", got)
	}
}

func TestRedact_BearerAndBasic(t *testing.T) {
	cases := []string{
		"Authorization: Bearer abcdef123",
		"authorization: basic dXNlcjpwYXNz",
	}
	for _, in := range cases {
		got := Redact(in)
		if strings.Contains(got, "abcdef") || strings.Contains(got, "dXNlcjpwYXNz") {
			t.Errorf("auth token leaked: input=%q got=%q", in, got)
		}
	}
}

func TestRedact_YAMLSecret(t *testing.T) {
	in := `apiVersion: v1
kind: Secret
data:
  password: c2VjcmV0
  api-token: bXlrZXk=
  not-sensitive: hello
`
	got := Redact(in)
	if strings.Contains(got, "c2VjcmV0") || strings.Contains(got, "bXlrZXk=") {
		t.Errorf("secret value leaked: %q", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("non-sensitive value should pass through: %q", got)
	}
}

func TestRedact_EnvVar(t *testing.T) {
	in := "MY_SECRET=topsecret\nNORMAL=hello"
	got := Redact(in)
	if strings.Contains(got, "topsecret") {
		t.Errorf("env secret leaked: %q", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("non-secret env should pass: %q", got)
	}
}

func TestTruncateLogs(t *testing.T) {
	lines := []string{}
	for i := 0; i < 500; i++ {
		lines = append(lines, "log line")
	}
	in := strings.Join(lines, "\n")
	got := TruncateLogs(in, 100)
	if !strings.Contains(got, "earlier lines truncated") {
		t.Errorf("expected truncation marker in: %q", got[:80])
	}
	if strings.Count(got, "\n") > 102 {
		t.Errorf("expected ~100 retained lines, got %d", strings.Count(got, "\n"))
	}
}
