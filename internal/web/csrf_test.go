package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCSRFEnsureCookieIssuesNewToken(t *testing.T) {
	s := newCSRFStore()
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	tok := s.ensureCookie(rr, r)
	if tok == "" {
		t.Fatalf("expected non-empty token")
	}
	found := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == csrfCookieName && c.Value == tok {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Set-Cookie %s=%s", csrfCookieName, tok)
	}
	if !s.valid(tok) {
		t.Errorf("issued token should be valid")
	}
}

func TestCSRFEnsureCookieReusesIssued(t *testing.T) {
	s := newCSRFStore()
	first := s.ensureCookie(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: first})
	rr := httptest.NewRecorder()
	if got := s.ensureCookie(rr, r); got != first {
		t.Fatalf("expected the issued token back, got %q", got)
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Errorf("did not expect Set-Cookie when a valid one is present")
	}
}

// Regression: a cookie planted by another localhost port is not adopted.
func TestCSRFEnsureCookieRejectsPlanted(t *testing.T) {
	s := newCSRFStore()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "attacker-chosen"})
	rr := httptest.NewRecorder()
	tok := s.ensureCookie(rr, r)
	if tok == "attacker-chosen" || s.valid("attacker-chosen") {
		t.Fatalf("planted token was adopted")
	}
	if len(rr.Result().Cookies()) != 1 {
		t.Errorf("expected the planted cookie to be overwritten")
	}
}

func TestCSRFStoreIsBounded(t *testing.T) {
	s := newCSRFStore()
	first := s.ensureCookie(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	for i := 0; i < maxCSRFTokens+500; i++ {
		s.ensureCookie(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}
	s.mu.Lock()
	n := len(s.issued)
	s.mu.Unlock()
	if n > maxCSRFTokens {
		t.Fatalf("store grew to %d", n)
	}
	if s.valid(first) {
		t.Fatalf("oldest token should have been evicted")
	}
}

func TestCSRFTokenExpires(t *testing.T) {
	s := newCSRFStore()
	s.ttl = 10 * time.Millisecond
	tok := s.ensureCookie(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	time.Sleep(30 * time.Millisecond)
	if s.valid(tok) {
		t.Fatalf("expired token still valid")
	}
}
