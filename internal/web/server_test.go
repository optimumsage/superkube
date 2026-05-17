package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/optimumsage/superkube/internal/audit"
	"github.com/optimumsage/superkube/internal/guardrail"
)

// newTestServer builds a Server suitable for httptest: no real kube/runner,
// just enough Deps to exercise routing and middleware.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	deps := Deps{
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

func TestStaticAssetsAreServed(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/static/css/app.css", nil)
	req.Host = "127.0.0.1"
	s.middleware(s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "--accent") {
		t.Errorf("expected app.css contents (looking for --accent CSS variable), got %q", rr.Body.String()[:min(80, rr.Body.Len())])
	}
}

func TestIndexSetsCSRFCookie(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1"
	s.middleware(s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !hasCookie(rr.Result().Cookies(), csrfCookieName) {
		t.Errorf("expected CSRF cookie")
	}
}

func TestDNSRebindProtection(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "evil.com"
	s.middleware(s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCSRFRejectsPOSTWithoutHeader(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/contexts/switch",
		strings.NewReader(`{"context":"x"}`))
	req.Host = "127.0.0.1"
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "abc"})
	// missing X-CSRF-Token header
	s.middleware(s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCSRFAcceptsMatchingHeader(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/switch",
		strings.NewReader(`{"namespace":"foo"}`))
	req.Host = "127.0.0.1"
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "abc"})
	req.Header.Set("X-CSRF-Token", "abc")
	// May still return non-2xx because the loader isn't real, but the CSRF
	// guard must not return 403 due to the header check.
	s.middleware(s.mux).ServeHTTP(rr, req)
	if rr.Code == http.StatusForbidden {
		t.Fatalf("CSRF guard rejected a valid header pair")
	}
}

func TestTokenRequiredWhenSet(t *testing.T) {
	deps := Deps{Policy: func() guardrail.Policy { return guardrail.Policy{} }, Banner: func() (string, string) { return "", "" }, Audit: func(audit.Event) {}}
	s, err := New(Config{Bind: "0.0.0.0", Token: "secret-token", NoOpen: true}, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// No token → 401.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/info", nil)
	req.Host = "0.0.0.0"
	s.middleware(s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", rr.Code)
	}
	// With token in query → not 401.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/info?token=secret-token", nil)
	req2.Host = "0.0.0.0"
	s.middleware(s.mux).ServeHTTP(rr2, req2)
	if rr2.Code == http.StatusUnauthorized {
		t.Fatalf("token in query should pass auth")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	s := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	// Give the server a tick to bind. We don't have a robust readiness signal,
	// but Run is fast.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-make(chan struct{}):
		// unreachable
	}
}

func hasCookie(cookies []*http.Cookie, name string) bool {
	for _, c := range cookies {
		if c.Name == name {
			return true
		}
	}
	return false
}

// avoid Go 1.21+ helper to keep this file's deps minimal
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// silence unused imports when only some tests in this file run.
var _ = io.Discard
