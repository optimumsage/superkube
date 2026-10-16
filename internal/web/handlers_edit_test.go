package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/optimumsage/superkube/internal/guardrail"
)

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

func TestEditRejectsCommentedAndCRLFSeparators(t *testing.T) {
	ok := func(y string) error { return validateEditYAML("cm", "default", "app", "default", y) }
	if err := ok(cmYAML); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	second := strings.Replace(cmYAML, "name: app", "name: other", 1)
	for name, y := range map[string]string{
		"commented sep": cmYAML + "--- # second object\n" + second,
		"crlf sep":      strings.ReplaceAll(cmYAML+"---\n"+second, "\n", "\r\n"),
		"crlf comment":  strings.ReplaceAll(cmYAML+"--- # x\n"+second, "\n", "\r\n"),
		"leading sep":   "---\n" + cmYAML + "---\n" + second,
	} {
		if ok(y) == nil {
			t.Errorf("%s: accepted two documents", name)
		}
	}
	if err := ok("---\n" + cmYAML + "---\n# only a comment\n"); err != nil {
		t.Errorf("trailing empty document should be fine: %v", err)
	}
}

func TestApplyForbidConsidersEveryDocument(t *testing.T) {
	pol := func(string) (guardrail.Policy, error) {
		return guardrail.Policy{Forbidden: []string{"apply secret", "apply -n kube-system"}}, nil
	}
	e := newHardenEnv(t, hardenOpts{policy: pol})
	cm := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n"
	sec := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n"
	ks := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: b\n  namespace: kube-system\n"
	for name, y := range map[string]string{
		"secret second": cm + "--- # more\n" + sec,
		"ns second":     cm + "---\n" + ks,
	} {
		rr := e.do(t, "/api/v1/apply/preview", mustJSONStr(map[string]string{"yaml": y}))
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s: code %d, want 403", name, rr.Code)
		}
	}
	if len(e.calls()) != 0 {
		t.Fatalf("kubectl ran: %v", e.calls())
	}
}
