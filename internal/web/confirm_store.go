package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"time"
)

// ptyConfirmStore tracks one-shot confirmation tokens for destructive
// operations. A handler that needs confirmation:
//
//  1. Reserves a token that records the verb, the kube context, and a SHA-256
//     digest of exactly what was previewed (canonical argv, or the YAML text).
//  2. Returns it to the client which then renders a modal.
//  3. Client re-submits with the token (and the typed value, if any).
//  4. Handler recomputes the digest from the new request and calls
//     Consume — true only if token, verb, context, digest and typed value all
//     match. A token minted for one operation therefore cannot authorize a
//     different one (a cordon token cannot apply YAML; a confirmed delete of
//     "foo" cannot delete "bar").
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

// maxConfirmEntries bounds memory: entries may carry multi-MiB YAML payloads.
const maxConfirmEntries = 256

type ptyConfirmEntry struct {
	Expect    string // typed value we expect to match (empty for yes/no)
	Verb      string // operation name, bound at preview time
	Context   string // kube context the preview ran against
	Resource  string // e.g. "pod/web-7", for audit + UI
	Digest    string // sha256 of the previewed operation (see digestOf)
	Payload   string // exact content to execute on commit (apply/edit YAML)
	CreatedAt time.Time
}

func newPtyConfirmStore(ttl time.Duration) *ptyConfirmStore {
	return &ptyConfirmStore{entries: make(map[string]ptyConfirmEntry), ttl: ttl}
}

// digestOf hashes parts with length prefixes so ("ab","c") and ("a","bc")
// cannot collide.
func digestOf(parts ...string) string {
	h := sha256.New()
	var n [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Issue mints a new token, records the entry, and returns the token. Caller
// is responsible for sending it (and the prompt details) back to the client.
// Expired entries are swept on every insert so abandoned dialogs don't leak.
func (s *ptyConfirmStore) Issue(e ptyConfirmEntry) string {
	tok := mintConfirmToken()
	e.CreatedAt = time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(e.CreatedAt)
	for len(s.entries) >= maxConfirmEntries {
		// Still full of live entries: evict the oldest.
		var oldest string
		var oldestAt time.Time
		for t, x := range s.entries {
			if oldest == "" || x.CreatedAt.Before(oldestAt) {
				oldest, oldestAt = t, x.CreatedAt
			}
		}
		delete(s.entries, oldest)
	}
	s.entries[tok] = e
	return tok
}

// Consume validates the token against the operation the caller is about to
// run. On success the entry is removed (single-use) and returned. The entry
// is also burned on a failed match so a client must restart the dialog.
func (s *ptyConfirmStore) Consume(token, typed, verb, kubeContext, digest string) (ptyConfirmEntry, bool) {
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
	if e.Verb != verb || e.Context != kubeContext || e.Digest != digest {
		return ptyConfirmEntry{}, false
	}
	if e.Expect != "" && typed != e.Expect {
		return ptyConfirmEntry{}, false
	}
	return e, true
}

func (s *ptyConfirmStore) sweepLocked(now time.Time) {
	for tok, e := range s.entries {
		if now.Sub(e.CreatedAt) > s.ttl {
			delete(s.entries, tok)
		}
	}
}

func (s *ptyConfirmStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func mintConfirmToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
