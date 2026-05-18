package cli

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
	got := maskSecretYAML(in)

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
	got := revealSecretYAML(in)

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
	got := revealSecretYAML(in)
	if !strings.Contains(got, "<invalid-base64>") {
		t.Errorf("expected <invalid-base64> sentinel:\n%s", got)
	}
}

func TestRevealSecretYAML_EmptyValue(t *testing.T) {
	in := `data:
  blank: ""
`
	got := revealSecretYAML(in)
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
	got := revealSecretYAML(in)
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
	got := maskSecretYAML(in)
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
	got := maskSecretYAML(in)
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
	got := maskSecretYAML(in)
	if !strings.Contains(got, "type: Opaque") {
		t.Errorf("type key after empty data was eaten:\n%s", got)
	}
	if strings.Contains(got, "base64 hidden") {
		t.Errorf("empty data map should not produce a mask placeholder:\n%s", got)
	}
}

func TestSplitDataLine(t *testing.T) {
	cases := []struct {
		in           string
		wantK, wantV string
		wantOK       bool
	}{
		{"  username: bW9oc2lu", "username", "bW9oc2lu", true},
		{"  blank: \"\"", "blank", "\"\"", true},
		{"    indented: deeper", "indented", "deeper", true},
		{"not-a-pair", "", "", false},
		{":", "", "", false},
	}
	for _, tc := range cases {
		k, v, ok := splitDataLine(tc.in)
		if k != tc.wantK || v != tc.wantV || ok != tc.wantOK {
			t.Errorf("splitDataLine(%q) = (%q, %q, %v); want (%q, %q, %v)",
				tc.in, k, v, ok, tc.wantK, tc.wantV, tc.wantOK)
		}
	}
}
