package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCSRFEnsureCookieIssuesNewToken(t *testing.T) {
	s := newCSRFStore()
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	tok := s.ensureCookie(rr, r)
	if tok == "" {
		t.Fatalf("expected non-empty token")
	}
	cookies := rr.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == csrfCookieName && c.Value == tok {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Set-Cookie %s=%s", csrfCookieName, tok)
	}
}

func TestCSRFEnsureCookieAdoptsExisting(t *testing.T) {
	s := newCSRFStore()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "preset-token"})
	rr := httptest.NewRecorder()
	tok := s.ensureCookie(rr, r)
	if tok != "preset-token" {
		t.Fatalf("expected preset token, got %q", tok)
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Errorf("did not expect Set-Cookie when one is already present")
	}
}
