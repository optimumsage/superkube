package kube

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apiwatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
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

// A pod that is Pending when the tail starts (rollout) must be tailed once it
// goes Running, not skipped forever.
func TestTailMultiFollowPendingThenRunning(t *testing.T) {
	pending := podWith("a", "main")
	pending.Status.Phase = corev1.PodPending
	cs := fake.NewSimpleClientset(pending)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &lockedBuf{}
	done := make(chan error, 1)
	go func() {
		done <- tailMulti(ctx, cs, MultiLogOpts{Namespace: "ns", Selector: "app=web", Follow: true}, out, nil)
	}()
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(out.String(), "[a] ") {
		t.Fatal("pending pod must not be streamed yet")
	}
	running := pending.DeepCopy()
	running.Status.Phase = corev1.PodRunning
	if _, err := cs.CoreV1().Pods("ns").Update(ctx, running, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 3*time.Second, func() bool { return strings.Contains(out.String(), "[a] ") }) {
		t.Fatalf("pod never tailed after going Running; output %q", out.String())
	}
	cancel()
	<-done
}

// A transient open failure is retried; a claim is not leaked on final failure.
func TestTailMultiRetriesTransientOpenFailure(t *testing.T) {
	logOpenRetryDelay = time.Millisecond
	defer func() { logOpenRetryDelay = time.Second }()
	cs := fake.NewSimpleClientset(podWith("a", "main"))
	var calls atomic.Int32
	open := func(context.Context, string, string, *int64) (io.ReadCloser, error) {
		if calls.Add(1) < 3 {
			return nil, errors.New("container is waiting to start")
		}
		return io.NopCloser(strings.NewReader("hello\n")), nil
	}
	var buf bytes.Buffer
	err := tailMultiWith(context.Background(), cs, MultiLogOpts{Namespace: "ns", Selector: "app=web"}, &buf, nil, open)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || !strings.Contains(buf.String(), "hello") {
		t.Fatalf("calls=%d out=%q", calls.Load(), buf.String())
	}
}

func TestTailMultiOpenFailureReportedAfterRetries(t *testing.T) {
	logOpenRetryDelay = time.Millisecond
	defer func() { logOpenRetryDelay = time.Second }()
	cs := fake.NewSimpleClientset(podWith("a", "main"))
	open := func(context.Context, string, string, *int64) (io.ReadCloser, error) {
		return nil, errors.New("boom")
	}
	var buf bytes.Buffer
	_ = tailMultiWith(context.Background(), cs, MultiLogOpts{Namespace: "ns", Selector: "app=web"}, &buf, nil, open)
	if !strings.Contains(buf.String(), "error opening log stream: boom") {
		t.Fatalf("final failure not reported: %q", buf.String())
	}
}

func forbidden() error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("no"))
}

// Forbidden when establishing the watch is permanent: surfaced, not retried.
func TestTailMultiFollowWatchForbiddenReturns(t *testing.T) {
	cs := fake.NewSimpleClientset(podWith("a", "main"))
	cs.PrependWatchReactor("pods", func(k8stesting.Action) (bool, apiwatch.Interface, error) {
		return true, nil, forbidden()
	})
	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- tailMulti(context.Background(), cs, MultiLogOpts{Namespace: "ns", Selector: "app=web", Follow: true, Warn: &buf}, &buf, nil)
	}()
	select {
	case err := <-done:
		if !apierrors.IsForbidden(errors.Unwrap(err)) && !strings.Contains(err.Error(), "forbidden") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("forbidden watch was retried forever")
	}
}

// watch.Error events: Forbidden is fatal; other errors are surfaced and
// trigger a resync instead of being discarded.
func TestWatchNewPodsErrorEvents(t *testing.T) {
	// Fatal: Forbidden status event.
	cs := fake.NewSimpleClientset()
	fw := apiwatch.NewFake()
	cs.PrependWatchReactor("pods", k8stesting.DefaultWatchReactor(fw, nil))
	go fw.Error(&forbidden().(*apierrors.StatusError).ErrStatus)
	err := watchNewPods(context.Background(), cs, "ns", "app=web", "1", func(*corev1.Pod) {}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("forbidden event should be fatal, got %v", err)
	}

	// Transient: warned, then the loop resyncs and keeps going until ctx ends.
	cs2 := fake.NewSimpleClientset()
	fw2 := apiwatch.NewFake()
	cs2.PrependWatchReactor("pods", k8stesting.DefaultWatchReactor(fw2, nil))
	var warned atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- watchNewPods(ctx, cs2, "ns", "app=web", "1", func(*corev1.Pod) {}, func(string) { warned.Add(1) })
	}()
	fw2.Error(&apierrors.NewInternalError(errors.New("etcd hiccup")).ErrStatus)
	if !waitFor(t, 2*time.Second, func() bool { return warned.Load() == 1 }) {
		t.Fatal("transient Error event was not surfaced")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("transient error must not be fatal: %v", err)
	}
}
