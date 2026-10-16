package web

import (
	"testing"
	"time"
)

func issue(store *ptyConfirmStore, verb, expect string) string {
	return store.Issue(ptyConfirmEntry{Verb: verb, Context: "ctx", Resource: "r", Expect: expect, Digest: "d"})
}

func TestPtyConfirmStoreSingleUse(t *testing.T) {
	store := newPtyConfirmStore(30 * time.Second)
	tok := issue(store, "delete", "foo")
	if _, ok := store.Consume(tok, "foo", "delete", "ctx", "d"); !ok {
		t.Fatalf("first consume should succeed")
	}
	if _, ok := store.Consume(tok, "foo", "delete", "ctx", "d"); ok {
		t.Fatalf("second consume should fail (single-use)")
	}
}

func TestPtyConfirmStoreMismatchedValue(t *testing.T) {
	store := newPtyConfirmStore(30 * time.Second)
	tok := issue(store, "delete", "foo")
	if _, ok := store.Consume(tok, "bar", "delete", "ctx", "d"); ok {
		t.Fatalf("mismatched value should fail")
	}
	// After failure the entry is gone — the client must start a new dialog.
	if _, ok := store.Consume(tok, "foo", "delete", "ctx", "d"); ok {
		t.Fatalf("token should be invalidated after a failed match")
	}
}

func TestPtyConfirmStoreExpiry(t *testing.T) {
	store := newPtyConfirmStore(10 * time.Millisecond)
	tok := issue(store, "scale", "")
	time.Sleep(30 * time.Millisecond)
	if _, ok := store.Consume(tok, "", "scale", "ctx", "d"); ok {
		t.Fatalf("expired token should fail")
	}
}

func TestPtyConfirmStoreYesNoNeedsNoValue(t *testing.T) {
	store := newPtyConfirmStore(30 * time.Second)
	tok := issue(store, "scale", "")
	if _, ok := store.Consume(tok, "", "scale", "ctx", "d"); !ok {
		t.Fatalf("yes/no confirm (no Expect) should accept empty value")
	}
}

// Regression: a token minted for one operation must not authorize another.
func TestPtyConfirmStoreBindsVerbContextDigest(t *testing.T) {
	cases := []struct{ name, verb, ctx, digest string }{
		{"other verb", "apply", "ctx", "d"},
		{"other context", "cordon", "prod", "d"},
		{"other digest", "cordon", "ctx", "other"},
	}
	for _, c := range cases {
		store := newPtyConfirmStore(30 * time.Second)
		tok := issue(store, "cordon", "")
		if _, ok := store.Consume(tok, "", c.verb, c.ctx, c.digest); ok {
			t.Errorf("%s: token accepted", c.name)
		}
	}
}

func TestPtyConfirmStoreSweepsOnIssue(t *testing.T) {
	store := newPtyConfirmStore(5 * time.Millisecond)
	for i := 0; i < 10; i++ {
		issue(store, "scale", "")
	}
	time.Sleep(20 * time.Millisecond)
	issue(store, "scale", "")
	if n := store.len(); n != 1 {
		t.Fatalf("expired entries leaked: %d entries", n)
	}
}

func TestPtyConfirmStoreCapped(t *testing.T) {
	store := newPtyConfirmStore(time.Minute)
	for i := 0; i < maxConfirmEntries+50; i++ {
		issue(store, "scale", "")
	}
	if n := store.len(); n > maxConfirmEntries {
		t.Fatalf("store grew to %d", n)
	}
}

func TestDigestOfNoConcatCollision(t *testing.T) {
	if digestOf("ab", "c") == digestOf("a", "bc") {
		t.Fatal("digest collides across part boundaries")
	}
}
