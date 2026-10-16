package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeKubeconfig(t *testing.T, path, current string) {
	t.Helper()
	kc := `apiVersion: v1
kind: Config
current-context: ` + current + `
clusters:
- name: c
  cluster: {server: "https://127.0.0.1:1"}
users:
- name: u
  user: {token: x}
contexts:
- name: dev
  context: {cluster: c, user: u, namespace: devns}
- name: prod
  context: {cluster: c, user: u}
`
	if err := os.WriteFile(path, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionSnapshotPinsContextAndNamespace(t *testing.T) {
	kc := filepath.Join(t.TempDir(), "kc")
	writeKubeconfig(t, kc, "dev")
	e := newHardenEnv(t, hardenOpts{kubeconfig: kc, policy: prodForbidsDelete})
	body := `{"kind":"pod","name":"foo"}`
	first := decode[confirmationResponse](t, e.do(t, "/api/v1/destructive/delete", body))
	if first.Status != "needs_confirmation" {
		t.Fatalf("first = %+v", first)
	}
	// current-context flips to prod between confirm and exec.
	writeKubeconfig(t, kc, "prod")
	rr := e.do(t, "/api/v1/destructive/delete", `{"kind":"pod","name":"foo","confirm_token":"`+first.Token+`","confirm_value":"foo"}`)
	if rr.Code == 200 && len(e.calls()) > 0 {
		t.Fatalf("token minted for dev must not execute against prod: %s", e.calls())
	}
	// A fresh dev-pinned request carries explicit --context and -n.
	writeKubeconfig(t, kc, "dev")
	first = decode[confirmationResponse](t, e.do(t, "/api/v1/destructive/delete", body))
	e.do(t, "/api/v1/destructive/delete", `{"kind":"pod","name":"foo","confirm_token":"`+first.Token+`","confirm_value":"foo"}`)
	c := e.calls()
	if len(c) != 1 || !strings.Contains(c[0], "--context dev") || !strings.Contains(c[0], "-n devns") {
		t.Fatalf("calls = %q", c)
	}
}

func TestDigestsBindEffectiveNamespace(t *testing.T) {
	a, b := session{Namespace: "a"}, session{Namespace: "b"}
	if yamlDigest(a, "apply", "", "x") == yamlDigest(b, "apply", "", "x") {
		t.Fatal("yaml digest ignores namespace")
	}
	if argvDigest(a, "delete", []string{"x"}) == argvDigest(b, "delete", []string{"x"}) {
		t.Fatal("argv digest ignores namespace")
	}
	if helmPreviewDigest(a, "helm.install", struct{ N string }{"x"}) == helmPreviewDigest(b, "helm.install", struct{ N string }{"x"}) {
		t.Fatal("helm digest ignores namespace")
	}
}
