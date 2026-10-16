package tui

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func eventually(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// An error on a kind with no objects (so no events) must clear once the list
// starts succeeding again.
func TestWatchErrorClearsAfterRecoveryOnEmptyKind(t *testing.T) {
	cs := fake.NewSimpleClientset()
	var failing atomic.Bool
	failing.Store(true)
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
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
	if !eventually(t, 5*time.Second, func() bool { _, e := state.KindStatus(KindPod); return e != "" }) {
		t.Fatal("error never surfaced")
	}
	failing.Store(false)
	if !eventually(t, 10*time.Second, func() bool {
		synced, e := state.KindStatus(KindPod)
		return synced && e == ""
	}) {
		s, e := state.KindStatus(KindPod)
		t.Fatalf("error never cleared after recovery (synced=%v err=%q)", s, e)
	}
}

// SetSynced must not erase an error: list can succeed while watch is forbidden.
func TestSetSyncedDoesNotClearError(t *testing.T) {
	s := NewState()
	s.SetError(KindPod, "watch forbidden")
	s.SetSynced(KindPod)
	if _, e := s.KindStatus(KindPod); e == "" {
		t.Fatal("SetSynced clobbered a watch error")
	}
	s.ClearError(KindPod)
	if _, e := s.KindStatus(KindPod); e != "" {
		t.Fatal("ClearError did not clear")
	}
}

// The returned stop func is idempotent and shuts informers down.
func TestStartInformersStopIdempotent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	ctx, cancel := context.WithCancel(context.Background())
	stop, err := StartInformers(ctx, cs, "", NewState())
	if err != nil {
		t.Fatal(err)
	}
	stop()
	stop()
	cancel() // ctx cancellation after stop must not double-close
	time.Sleep(50 * time.Millisecond)
}
