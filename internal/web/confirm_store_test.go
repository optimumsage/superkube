package web

import (
	"testing"
	"time"
)

func TestPtyConfirmStoreSingleUse(t *testing.T) {
	store := newPtyConfirmStore(30 * time.Second)
	tok := store.Issue(ptyConfirmEntry{Verb: "delete", Resource: "pod/foo", Expect: "foo"})
	if _, ok := store.Consume(tok, "foo"); !ok {
		t.Fatalf("first consume should succeed")
	}
	if _, ok := store.Consume(tok, "foo"); ok {
		t.Fatalf("second consume should fail (single-use)")
	}
}

func TestPtyConfirmStoreMismatchedValue(t *testing.T) {
	store := newPtyConfirmStore(30 * time.Second)
	tok := store.Issue(ptyConfirmEntry{Verb: "delete", Resource: "pod/foo", Expect: "foo"})
	if _, ok := store.Consume(tok, "bar"); ok {
		t.Fatalf("mismatched value should fail")
	}
	// After failure the entry is gone — subsequent retries with the right
	// value must also fail, forcing the client to start a new dialog.
	if _, ok := store.Consume(tok, "foo"); ok {
		t.Fatalf("token should be invalidated after a failed match")
	}
}

func TestPtyConfirmStoreExpiry(t *testing.T) {
	store := newPtyConfirmStore(10 * time.Millisecond)
	tok := store.Issue(ptyConfirmEntry{Verb: "scale", Resource: "deploy/foo"})
	time.Sleep(30 * time.Millisecond)
	if _, ok := store.Consume(tok, ""); ok {
		t.Fatalf("expired token should fail")
	}
}

func TestPtyConfirmStoreYesNoNeedsNoValue(t *testing.T) {
	store := newPtyConfirmStore(30 * time.Second)
	tok := store.Issue(ptyConfirmEntry{Verb: "scale", Resource: "deploy/foo"})
	if _, ok := store.Consume(tok, ""); !ok {
		t.Fatalf("yes/no confirm (no Expect) should accept empty value")
	}
}
