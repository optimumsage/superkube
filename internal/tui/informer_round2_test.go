package tui

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	apiwatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// LIST succeeds (so the kind syncs) but WATCH fails; once WATCH can be
// established again the error must clear even though nothing about the list
// changed (quiet cluster, unchanged resourceVersion).
func TestWatchErrorClearsAfterRecoveryAtUnchangedRV(t *testing.T) {
	cs := fake.NewSimpleClientset()
	var failing atomic.Bool
	failing.Store(true)
	cs.PrependWatchReactor("pods", func(k8stesting.Action) (bool, apiwatch.Interface, error) {
		if failing.Load() {
			return true, nil, errors.New("connection refused")
		}
		return false, nil, nil
	})
	state := NewState()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := StartInformers(ctx, cs, "", state)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !eventually(t, 5*time.Second, func() bool {
		synced, e := state.KindStatus(KindPod)
		return synced && e != ""
	}) {
		t.Fatal("expected synced kind with a watch error")
	}
	failing.Store(false)
	if !eventually(t, 15*time.Second, func() bool { _, e := state.KindStatus(KindPod); return e == "" }) {
		t.Fatal("error stayed after a successful watch re-establishment at unchanged RV")
	}
}

// A success that began before a newer failure must not erase that failure.
func TestClearErrorIfGenKeepsNewerFailure(t *testing.T) {
	s := NewState()
	s.SetError(KindPod, "old")
	gen := s.ErrGen(KindPod) // a successful call starts here...
	s.SetError(KindPod, "newer failure")
	// ...and completes after the newer failure was recorded.
	if s.ClearErrorIfGen(KindPod, gen) {
		t.Fatal("stale success cleared a newer error")
	}
	if _, e := s.KindStatus(KindPod); e != "newer failure" {
		t.Fatalf("error = %q", e)
	}
	gen = s.ErrGen(KindPod)
	if !s.ClearErrorIfGen(KindPod, gen) {
		t.Fatal("up-to-date success should clear")
	}
	if _, e := s.KindStatus(KindPod); e != "" {
		t.Fatal("error not cleared")
	}
}
