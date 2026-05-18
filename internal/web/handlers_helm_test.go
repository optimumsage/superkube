package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/optimumsage/superkube/internal/audit"
	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/helm"
)

// fakeHelmBinary writes a small shell script that prints its args and exits 0.
// Returns the path; t.TempDir cleans it up automatically.
func fakeHelmBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake helm binary requires a POSIX shell")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-helm")
	body := `#!/bin/sh
echo "fake-helm called with: $@"
exit 0
`
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake helm: %v", err)
	}
	return bin
}

// newTestServerWithHelm builds a server whose Deps.Helm points at a fake
// binary so the confirmation flow can be exercised without the real helm.
func newTestServerWithHelm(t *testing.T) *Server {
	t.Helper()
	bin := fakeHelmBinary(t)
	deps := Deps{
		Helm:   &helm.Runner{Path: bin},
		Policy: func() guardrail.Policy { return guardrail.Policy{} },
		Banner: func() (string, string) { return "", "" },
		Audit:  func(audit.Event) {},
	}
	s, err := New(Config{Bind: "127.0.0.1", NoOpen: true}, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestHelmStatus_NoHelm(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/helm/status", nil)
	req.Host = "127.0.0.1"
	s.middleware(s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["installed"] != false {
		t.Fatalf("installed = %v, want false", body["installed"])
	}
}

func TestHelmReleases_NoHelm_503(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/helm/releases", nil)
	req.Host = "127.0.0.1"
	s.middleware(s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHelmRollback_RequiresConfirmation(t *testing.T) {
	s := newTestServerWithHelm(t)
	body := `{"name":"my-app","namespace":"default","revision":2}`
	rr := postJSON(t, s, "/api/v1/helm/rollback", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp confirmationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "needs_confirmation" || resp.Style != "yes_no" || resp.Token == "" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestHelmRollback_TokenCommits(t *testing.T) {
	s := newTestServerWithHelm(t)
	// First call: get a token.
	body := `{"name":"my-app","namespace":"default","revision":2}`
	rr := postJSON(t, s, "/api/v1/helm/rollback", body)
	var resp confirmationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Token == "" {
		t.Fatalf("no token in response: %s", rr.Body.String())
	}
	// Second call: include the token; expect the fake helm to be invoked.
	commitBody := `{"name":"my-app","namespace":"default","revision":2,"confirm_token":"` + resp.Token + `"}`
	rr2 := postJSON(t, s, "/api/v1/helm/rollback", commitBody)
	if rr2.Code != http.StatusOK {
		t.Fatalf("commit status = %d, want 200; body=%s", rr2.Code, rr2.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr2.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(out["output"].(string), "rollback") {
		t.Errorf("expected fake-helm to print rollback args, got %q", out["output"])
	}
}

func TestHelmUninstall_TypedNameConfirm(t *testing.T) {
	s := newTestServerWithHelm(t)
	body := `{"name":"my-app","namespace":"default"}`
	rr := postJSON(t, s, "/api/v1/helm/uninstall", body)
	var resp confirmationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Style != "typed_name" || resp.Expect != "my-app" {
		t.Fatalf("expected typed_name confirm for my-app, got %+v", resp)
	}
	// Wrong name → 410.
	bad := `{"name":"my-app","namespace":"default","confirm_token":"` + resp.Token + `","confirm_value":"WRONG"}`
	rrBad := postJSON(t, s, "/api/v1/helm/uninstall", bad)
	if rrBad.Code != http.StatusGone {
		t.Fatalf("wrong name status = %d, want 410; body=%s", rrBad.Code, rrBad.Body.String())
	}
}

func TestHelmRepoAdd_RedactsPasswordInAudit(t *testing.T) {
	captured := []audit.Event{}
	bin := fakeHelmBinary(t)
	deps := Deps{
		Helm:   &helm.Runner{Path: bin},
		Policy: func() guardrail.Policy { return guardrail.Policy{} },
		Banner: func() (string, string) { return "", "" },
		Audit:  func(e audit.Event) { captured = append(captured, e) },
	}
	s, err := New(Config{Bind: "127.0.0.1", NoOpen: true}, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := `{"name":"bitnami","url":"https://charts.bitnami.com/bitnami","username":"u","password":"hunter2"}`
	rr := postJSON(t, s, "/api/v1/helm/repos", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if len(captured) != 1 {
		t.Fatalf("want 1 audit event, got %d", len(captured))
	}
	for _, a := range captured[0].Argv {
		if a == "hunter2" {
			t.Errorf("password leaked to audit: %v", captured[0].Argv)
		}
	}
}

func TestHelmRollback_BadInput(t *testing.T) {
	s := newTestServerWithHelm(t)
	// Missing revision.
	body := `{"name":"x","namespace":"y"}`
	rr := postJSON(t, s, "/api/v1/helm/rollback", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// postJSON sends a CSRF-cleared POST through the middleware stack so handler
// tests don't have to re-implement the cookie dance every time.
func postJSON(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Host = "127.0.0.1"
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "t"})
	req.Header.Set("X-CSRF-Token", "t")
	s.middleware(s.mux).ServeHTTP(rr, req)
	return rr
}
