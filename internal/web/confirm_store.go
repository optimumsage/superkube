package web

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// ptyConfirmStore tracks one-shot confirmation tokens for destructive
// operations. A handler that needs typed confirmation:
//
//  1. Reserves a token + records the expected payload (resource name, etc.)
//  2. Returns it to the client which then renders a modal.
//  3. Client re-submits with the token and the typed value.
//  4. Handler calls Consume(token, value) — true means proceed, false means
//     mismatch (wrong typed value, expired, already used, or never existed).
//
// Tokens are single-use to prevent replays. TTL defends against tabs left
// open. The naming "ptyConfirm" hints at the CLI equivalent — guardrail.YesNo
// and friends, which run on a terminal pty — but the implementation is
// in-memory and stateless across server restarts.
type ptyConfirmStore struct {
	mu      sync.Mutex
	entries map[string]ptyConfirmEntry
	ttl     time.Duration
}

type ptyConfirmEntry struct {
	Expect    string // typed value we expect to match (empty for yes/no)
	Verb      string // operation name, for audit
	Resource  string // e.g. "pod/web-7", for audit + UI
	Argv      []string
	CreatedAt time.Time
}

func newPtyConfirmStore(ttl time.Duration) *ptyConfirmStore {
	return &ptyConfirmStore{entries: make(map[string]ptyConfirmEntry), ttl: ttl}
}

// Issue mints a new token, records the entry, and returns the token. Caller
// is responsible for sending it (and the prompt details) back to the client.
func (s *ptyConfirmStore) Issue(e ptyConfirmEntry) string {
	tok := mintConfirmToken()
	e.CreatedAt = time.Now()
	s.mu.Lock()
	s.entries[tok] = e
	s.mu.Unlock()
	return tok
}

// Consume validates the token + typed value. On success the entry is removed
// (single-use) and the original entry is returned.
func (s *ptyConfirmStore) Consume(token, typed string) (ptyConfirmEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[token]
	if !ok {
		return ptyConfirmEntry{}, false
	}
	delete(s.entries, token)
	if time.Since(e.CreatedAt) > s.ttl {
		return ptyConfirmEntry{}, false
	}
	if e.Expect != "" && typed != e.Expect {
		return ptyConfirmEntry{}, false
	}
	return e, true
}

// sweep is a best-effort GC that callers may invoke periodically. We don't
// run a goroutine — the entry count is tiny and idle servers don't matter.
func (s *ptyConfirmStore) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for tok, e := range s.entries {
		if now.Sub(e.CreatedAt) > s.ttl {
			delete(s.entries, tok)
		}
	}
}

func mintConfirmToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
