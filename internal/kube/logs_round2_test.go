package kube

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	apiwatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// 401 is as permanent as 403: fail on the first attempt instead of retrying.
func TestTailMultiUnauthorizedFailsFast(t *testing.T) {
	cs := fake.NewSimpleClientset(podWith("a", "main"))
	var calls atomic.Int32
	open := func(context.Context, string, string, *int64) (io.ReadCloser, error) {
		calls.Add(1)
		return nil, apierrors.NewUnauthorized("expired token")
	}
	var buf bytes.Buffer
	_ = tailMultiWith(context.Background(), cs, MultiLogOpts{Namespace: "ns", Selector: "app=web", OpenRetryDelay: time.Hour}, &buf, nil, open)
	if calls.Load() != 1 {
		t.Fatalf("unauthorized was retried: %d calls", calls.Load())
	}
	if !strings.Contains(buf.String(), "error opening log stream") {
		t.Fatalf("not reported: %q", buf.String())
	}
}

// A non-auth List failure during resync must be surfaced, not silent.
func TestWatchNewPodsWarnsOnRelistFailure(t *testing.T) {
	cs := fake.NewSimpleClientset()
	fw := apiwatch.NewFake()
	cs.PrependWatchReactor("pods", k8stesting.DefaultWatchReactor(fw, nil))
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("apiserver restarting")
	})
	var warned atomic.Int32
	var msg atomic.Value
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- watchNewPods(ctx, cs, "ns", "app=web", "1", func(*corev1.Pod) {}, func(m string) {
			msg.Store(m)
			warned.Add(1)
		})
	}()
	fw.Stop() // watch ends -> back off -> re-list (fails)
	if !waitFor(t, 4*time.Second, func() bool { return warned.Load() > 0 }) {
		t.Fatal("re-list failure was silent")
	}
	if m, _ := msg.Load().(string); !strings.Contains(m, "re-list") {
		t.Fatalf("unexpected warning %q", m)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
