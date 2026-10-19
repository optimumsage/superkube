package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/optimumsage/superkube/internal/kubectl"
)

const secretListYAML = `apiVersion: v1
kind: List
items:
- apiVersion: v1
  kind: Secret
  metadata:
    name: a
    annotations:
      kubectl.kubernetes.io/last-applied-configuration: '{"data":{"pw":"c2VjcmV0"}}'
  data:
    pw: c2VjcmV0
`

func secretEnv(t *testing.T) *hardenEnv {
	e := newHardenEnv(t, hardenOpts{})
	dir := t.TempDir()
	f := filepath.Join(dir, "yaml")
	if err := os.WriteFile(f, []byte(secretListYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat "+f+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.s.deps.Runner = &kubectl.Runner{Path: bin}
	return e
}

func TestSecretYAMLMaskedUnlessRevealed(t *testing.T) {
	e := secretEnv(t)
	masked := e.doMethod(t, "GET", "/api/v1/resources/secrets/default/a/yaml", "").Body.String()
	if strings.Contains(masked, "c2VjcmV0") || strings.Contains(masked, "secret") && !strings.Contains(masked, "kind: Secret") {
		t.Fatalf("unrevealed secret leaked:\n%s", masked)
	}
	rev := e.doMethod(t, "GET", "/api/v1/resources/secrets/default/a/yaml?reveal=1", "")
	if rev.Code != http.StatusOK || strings.Contains(rev.Body.String(), "c2VjcmV0") {
		t.Fatalf("revealed path leaked base64 (incl. last-applied annotation):\n%s", rev.Body.String())
	}
	if !strings.Contains(rev.Body.String(), "pw: secret") {
		t.Fatalf("reveal did not decode:\n%s", rev.Body.String())
	}
}
