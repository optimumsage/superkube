package kube

import (
	"context"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiwatch "k8s.io/apimachinery/pkg/watch"
)

// RetryWatcher retries an establishment-time 410 forever with the same
// resourceVersion. The loop must instead notice it and re-list.
func TestListWatchLoopRelistsOnEstablishmentGone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var mu sync.Mutex
	var fetchRVs, watchRVs []string
	fetch := func(context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		fetchRVs = append(fetchRVs, "rv"+string(rune('0'+len(fetchRVs)+1)))
		return fetchRVs[len(fetchRVs)-1], nil
	}
	w := watcherFunc(func(_ context.Context, o metav1.ListOptions) (apiwatch.Interface, error) {
		mu.Lock()
		watchRVs = append(watchRVs, o.ResourceVersion)
		n := len(fetchRVs)
		mu.Unlock()
		if n >= 2 {
			cancel() // second list happened: success, stop the loop
		}
		return nil, apierrors.NewResourceExpired("too old")
	})

	done := make(chan error, 1)
	go func() { done <- listWatchLoop(ctx, fetch, w, func() {}, time.Millisecond) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("loop error: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("loop stuck on establishment-time Gone (no re-list)")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(fetchRVs) < 2 {
		t.Fatalf("fetches = %v, want a re-list", fetchRVs)
	}
	if len(watchRVs) < 2 || watchRVs[0] == watchRVs[1] {
		t.Fatalf("watch rvs = %v, want fresh resourceVersion after re-list", watchRVs)
	}
}
