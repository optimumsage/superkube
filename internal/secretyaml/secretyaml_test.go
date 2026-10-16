package secretyaml

import (
	"strings"
	"testing"
)

func TestMaskSecretYAML(t *testing.T) {
	in := `apiVersion: v1
kind: Secret
metadata:
  name: my-creds
  namespace: default
data:
  username: bW9oc2lu
  password: c2VjcmV0
type: Opaque
`
	got := Mask(in)

	// Structure is preserved: keys, metadata, type all survive verbatim.
	for _, want := range []string{
		"apiVersion: v1",
		"kind: Secret",
		"name: my-creds",
		"type: Opaque",
		"  username:",
		"  password:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("masked output missing %q\n%s", want, got)
		}
	}
	// Values are gone.
	for _, leaked := range []string{"bW9oc2lu", "c2VjcmV0"} {
		if strings.Contains(got, leaked) {
			t.Errorf("masked output leaked %q\n%s", leaked, got)
		}
	}
	if !strings.Contains(got, "base64 hidden") {
		t.Errorf("masked output missing placeholder hint:\n%s", got)
	}
}

func TestRevealSecretYAML(t *testing.T) {
	in := `apiVersion: v1
kind: Secret
metadata:
  name: my-creds
data:
  username: bW9oc2lu
  password: c2VjcmV0
type: Opaque
`
	got := Reveal(in)

	if !strings.Contains(got, "username: mohsin") {
		t.Errorf("expected decoded username=mohsin:\n%s", got)
	}
	if !strings.Contains(got, "password: secret") {
		t.Errorf("expected decoded password=secret:\n%s", got)
	}
	if strings.Contains(got, "bW9oc2lu") || strings.Contains(got, "c2VjcmV0") {
		t.Errorf("revealed output still contains base64 source:\n%s", got)
	}
}

func TestRevealSecretYAML_InvalidBase64(t *testing.T) {
	in := `data:
  bad: "@not-base64@"
`
	got := Reveal(in)
	if !strings.Contains(got, "<invalid-base64>") {
		t.Errorf("expected <invalid-base64> sentinel:\n%s", got)
	}
}

func TestRevealSecretYAML_EmptyValue(t *testing.T) {
	in := `data:
  blank: ""
`
	got := Reveal(in)
	if !strings.Contains(got, "blank: \"\"") {
		t.Errorf("empty value should round-trip as \"\":\n%s", got)
	}
}

func TestRevealSecretYAML_MultilineDecoded(t *testing.T) {
	// "line1\nline2\n" → base64
	// echo -ne "line1\nline2\n" | base64  →  bGluZTEKbGluZTIK
	in := `data:
  cert: bGluZTEKbGluZTIK
`
	got := Reveal(in)
	// Multi-line decoded values should use the YAML block-scalar form.
	if !strings.Contains(got, "cert: |") {
		t.Errorf("expected block scalar marker `|`:\n%s", got)
	}
	if !strings.Contains(got, "    line1") || !strings.Contains(got, "    line2") {
		t.Errorf("expected indented lines:\n%s", got)
	}
}

func TestTransformSecretYAML_NoDataBlock(t *testing.T) {
	in := `apiVersion: v1
kind: Service
metadata:
  name: web
spec:
  ports:
  - port: 80
`
	got := Mask(in)
	if got != in {
		t.Errorf("manifests without data block should pass through unchanged.\nGot:\n%s", got)
	}
}

func TestTransformSecretYAML_BinaryData(t *testing.T) {
	// binaryData lives at the same level as data and follows the same
	// encoding convention. Make sure we mask it too.
	in := `apiVersion: v1
kind: Secret
binaryData:
  ca.crt: ZmFrZQ==
`
	got := Mask(in)
	if strings.Contains(got, "ZmFrZQ==") {
		t.Errorf("binaryData value leaked:\n%s", got)
	}
}

func TestTransformSecretYAML_EmptyDataMap(t *testing.T) {
	// `data: {}` is a valid empty form. We shouldn't try to mask anything,
	// and we shouldn't treat following lines as part of the (nonexistent)
	// data block.
	in := `data: {}
type: Opaque
`
	got := Mask(in)
	if !strings.Contains(got, "type: Opaque") {
		t.Errorf("type key after empty data was eaten:\n%s", got)
	}
	if strings.Contains(got, "base64 hidden") {
		t.Errorf("empty data map should not produce a mask placeholder:\n%s", got)
	}
}

func TestMaskSecretYAML_LastAppliedAnnotation(t *testing.T) {
	in := `apiVersion: v1
kind: Secret
metadata:
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: |
      {"apiVersion":"v1","data":{"password":"c3VwZXJzZWNyZXQ="},"kind":"Secret"}
  name: s
data:
  password: c3VwZXJzZWNyZXQ=
`
	for name, fn := range map[string]func(string) string{"mask": Mask, "reveal": Reveal} {
		got := fn(in)
		if strings.Contains(got, "c3VwZXJzZWNyZXQ=") {
			t.Errorf("%s: annotation leaked base64:\n%s", name, got)
		}
		if !strings.Contains(got, "last-applied-configuration") {
			t.Errorf("%s: annotation key should remain:\n%s", name, got)
		}
	}
}

func TestMaskSecretYAML_ListAndStringData(t *testing.T) {
	in := `apiVersion: v1
items:
- apiVersion: v1
  data:
    password: c2VjcmV0MQ==
  kind: Secret
  metadata:
    name: a
  stringData:
    token: plaintext-one
- apiVersion: v1
  data:
    password: c2VjcmV0Mg==
  kind: Secret
  metadata:
    name: b
kind: List
`
	got := Mask(in)
	for _, leaked := range []string{"c2VjcmV0MQ==", "c2VjcmV0Mg==", "plaintext-one"} {
		if strings.Contains(got, leaked) {
			t.Errorf("list output leaked %q:\n%s", leaked, got)
		}
	}
	for _, want := range []string{"name: a", "name: b", "password:", "token:"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestRevealSecretYAML_List(t *testing.T) {
	in := `items:
- data:
    password: c2VjcmV0MQ==
  kind: Secret
kind: List
`
	got := Reveal(in)
	if !strings.Contains(got, "password: secret1") {
		t.Errorf("list item not decoded:\n%s", got)
	}
}

func TestMaskSecretYAML_UnparseableFailsClosed(t *testing.T) {
	got := Mask("data:\n  a: [unterminated\n  b: c2VjcmV0\n")
	if strings.Contains(got, "c2VjcmV0") {
		t.Errorf("unparseable input leaked:\n%s", got)
	}
}
