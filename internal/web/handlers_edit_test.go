package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRevealSecretYAMLBytes_DecodesValues(t *testing.T) {
	in := []byte(`apiVersion: v1
kind: Secret
metadata:
  name: creds
data:
  username: bW9oc2lu
  password: c2VjcmV0
type: Opaque
`)
	got := revealSecretYAMLBytes(in)
	s := string(got)
	if !strings.Contains(s, "username: mohsin") {
		t.Errorf("expected decoded username:\n%s", s)
	}
	if !strings.Contains(s, "password: secret") {
		t.Errorf("expected decoded password:\n%s", s)
	}
	if bytes.Contains(got, []byte("bW9oc2lu")) {
		t.Errorf("base64 source leaked through:\n%s", s)
	}
	if !strings.Contains(s, "type: Opaque") {
		t.Errorf("tail of YAML eaten:\n%s", s)
	}
}

func TestRevealSecretYAMLBytes_InvalidBase64(t *testing.T) {
	in := []byte("data:\n  bad: \"@nope@\"\n")
	got := revealSecretYAMLBytes(in)
	if !bytes.Contains(got, []byte("<invalid-base64>")) {
		t.Errorf("expected <invalid-base64> sentinel: %s", got)
	}
}

func TestRevealSecretYAMLBytes_NoDataBlockUnchanged(t *testing.T) {
	in := []byte("apiVersion: v1\nkind: Service\nmetadata:\n  name: x\n")
	got := revealSecretYAMLBytes(in)
	if !bytes.Equal(got, in) {
		t.Errorf("input without data block was modified: %q", got)
	}
}

func TestIsSecretKind(t *testing.T) {
	cases := map[string]bool{
		"secret":     true,
		"secrets":    true,
		"configmaps": false,
		"pod":        false,
		"":           false,
	}
	for k, want := range cases {
		if isSecretKind(k) != want {
			t.Errorf("isSecretKind(%q) = %v, want %v", k, isSecretKind(k), want)
		}
	}
}

func TestEditableKinds(t *testing.T) {
	for _, k := range []string{"configmap", "configmaps", "cm", "secret", "secrets", "ingress", "ingresses", "deployments"} {
		if !editableKinds[k] {
			t.Errorf("editableKinds[%q] should be true", k)
		}
	}
	for _, k := range []string{"pod", "pods", "node", "namespace", ""} {
		if editableKinds[k] {
			t.Errorf("editableKinds[%q] should be false", k)
		}
	}
}

func TestFragResourceEdit_RejectsUnknownKind(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/frag/resources/pods/default/foo/edit", nil)
	req.Host = "127.0.0.1"
	req.SetPathValue("kind", "pods")
	req.SetPathValue("ns", "default")
	req.SetPathValue("name", "foo")
	s.fragResourceEdit(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for non-editable kind", rr.Code)
	}
}

func TestFragResourceEdit_RendersForConfigMap(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/frag/resources/configmaps/default/foo/edit", nil)
	req.Host = "127.0.0.1"
	req.SetPathValue("kind", "configmaps")
	req.SetPathValue("ns", "default")
	req.SetPathValue("name", "foo")
	s.fragResourceEdit(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "ResourceEditPage") {
		t.Errorf("expected ResourceEditPage Alpine component, got: %s", body)
	}
	if !strings.Contains(body, "configmaps") {
		t.Errorf("expected kind in template output, got: %s", body)
	}
}

func TestEditCommit_RejectsBadToken(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/resources/configmaps/default/foo/edit/commit",
		strings.NewReader(`{"yaml":"x","confirm_token":"nope"}`))
	req.Host = "127.0.0.1"
	req.SetPathValue("kind", "configmaps")
	req.SetPathValue("ns", "default")
	req.SetPathValue("name", "foo")
	s.apiResourceEditCommit(rr, req)
	if rr.Code != http.StatusGone {
		t.Errorf("status = %d, want 410 Gone for unknown token", rr.Code)
	}
}

func TestEditCommit_RejectsUnknownKind(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/resources/pods/default/foo/edit/commit",
		strings.NewReader(`{"yaml":"x","confirm_token":""}`))
	req.Host = "127.0.0.1"
	req.SetPathValue("kind", "pods")
	req.SetPathValue("ns", "default")
	req.SetPathValue("name", "foo")
	s.apiResourceEditCommit(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for non-editable kind", rr.Code)
	}
}

func TestEditPreview_RejectsUnknownKind(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/resources/pods/default/foo/edit/preview",
		strings.NewReader(`{"yaml":"apiVersion: v1\nkind: Pod\n"}`))
	req.Host = "127.0.0.1"
	req.SetPathValue("kind", "pods")
	req.SetPathValue("ns", "default")
	req.SetPathValue("name", "foo")
	s.apiResourceEditPreview(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for non-editable kind", rr.Code)
	}
}

func TestEditPreview_RejectsEmptyYAML(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/resources/configmaps/default/foo/edit/preview",
		strings.NewReader(`{"yaml":"  "}`))
	req.Host = "127.0.0.1"
	req.SetPathValue("kind", "configmaps")
	req.SetPathValue("ns", "default")
	req.SetPathValue("name", "foo")
	s.apiResourceEditPreview(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for empty yaml", rr.Code)
	}
}
