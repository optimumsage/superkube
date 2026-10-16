package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// csrfCookieName is the canonical name for the per-session CSRF cookie. HTMX
// echoes the cookie's value in X-CSRF-Token on every non-GET request (see
// static/index.html for the wiring).
const csrfCookieName = "sk_csrf"

// csrfTTL is how long an issued CSRF value stays valid without being used.
const csrfTTL = 12 * time.Hour

// csrfStore tracks the CSRF tokens this server instance has issued. A cookie
// value is only honoured if it is in the store: cookies are not port-isolated,
// so any other web server on localhost can plant `sk_csrf=<its choice>` in the
// browser and then send a matching header. Requiring server-issued values
// defeats that cookie-planting attack.
type csrfStore struct {
	mu     sync.Mutex
	issued map[string]time.Time // token -> last use
	ttl    time.Duration
}

func newCSRFStore() *csrfStore {
	return &csrfStore{issued: make(map[string]time.Time), ttl: csrfTTL}
}

// ensureCookie returns the request's CSRF token if it is one we issued and is
// still fresh; otherwise it mints a new one and overwrites the cookie. The
// HttpOnly bit is intentionally false: the UI reads the cookie via
// document.cookie to populate the X-CSRF-Token header.
func (s *csrfStore) ensureCookie(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookieName); err == nil && s.valid(c.Value) {
		return c.Value
	}
	tok := mintToken()
	s.mu.Lock()
	s.sweepLocked(time.Now())
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

// valid reports whether tok was issued by this server and has not expired,
// refreshing its last-use time when it has not.
func (s *csrfStore) valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.issued[tok]
	if !ok {
		return false
	}
	if time.Since(last) > s.ttl {
		delete(s.issued, tok)
		return false
	}
	s.issued[tok] = time.Now()
	return true
}

func (s *csrfStore) sweepLocked(now time.Time) {
	for tok, last := range s.issued {
		if now.Sub(last) > s.ttl {
			delete(s.issued, tok)
		}
	}
}

// matches reports whether header equals cookie in constant time.
func csrfMatches(cookie, header string) bool {
	return subtle.ConstantTimeCompare([]byte(cookie), []byte(header)) == 1
}

func mintToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
