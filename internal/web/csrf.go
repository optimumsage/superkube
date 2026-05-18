package web

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// csrfCookieName is the canonical name for the per-session CSRF cookie. HTMX
// echoes the cookie's value in X-CSRF-Token on every non-GET request (see
// static/index.html for the wiring).
const csrfCookieName = "sk_csrf"

// csrfStore tracks active CSRF tokens. We accept any cookie value we have
// previously issued; the cookie itself is the secret, the store is just there
// so handlers can render a fresh token into the page.
type csrfStore struct {
	mu     sync.Mutex
	issued map[string]time.Time
}

func newCSRFStore() *csrfStore {
	return &csrfStore{issued: make(map[string]time.Time)}
}

// ensureCookie returns the request's CSRF token, issuing a new cookie if the
// client didn't carry one yet. The HttpOnly bit is intentionally false: HTMX
// reads the cookie via document.cookie to populate the X-CSRF-Token header.
func (s *csrfStore) ensureCookie(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookieName); err == nil && c.Value != "" {
		s.touch(c.Value)
		return c.Value
	}
	tok := mintToken()
	s.mu.Lock()
	s.issued[tok] = time.Now()
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    tok,
		Path:     "/",
		SameSite: http.SameSiteStrictMode,
	})
	return tok
}

func (s *csrfStore) touch(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.issued[tok]; ok {
		s.issued[tok] = time.Now()
	} else {
		// Cookie predates this server run; accept it and adopt it.
		s.issued[tok] = time.Now()
	}
}

func mintToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
