package tui

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func makePod(ns, name string, phase corev1.PodPhase, ready, total int) *corev1.Pod {
	cs := make([]corev1.ContainerStatus, 0, total)
	for i := 0; i < total; i++ {
		cs = append(cs, corev1.ContainerStatus{
			Name:         "c",
			Ready:        i < ready,
			RestartCount: 0,
		})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
		Status: corev1.PodStatus{Phase: phase, ContainerStatuses: cs},
	}
}

func TestStateUpsertDeleteSnapshot(t *testing.T) {
	s := NewState()
	s.Upsert(makePod("default", "b", corev1.PodRunning, 1, 1))
	s.Upsert(makePod("default", "a", corev1.PodRunning, 1, 1))
	s.Upsert(makePod("kube-system", "z", corev1.PodPending, 0, 1))

	snap := s.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(snap))
	}
	// Sort order: namespace then name.
	want := []string{"default/a", "default/b", "kube-system/z"}
	for i, p := range snap {
		got := p.Namespace + "/" + p.Name
		if got != want[i] {
			t.Errorf("snap[%d] = %q, want %q", i, got, want[i])
		}
	}

	s.Delete(makePod("default", "a", corev1.PodRunning, 1, 1))
	if got := len(s.Snapshot()); got != 2 {
		t.Errorf("after delete len = %d, want 2", got)
	}
}

func TestStateUpdatesExistingPod(t *testing.T) {
	s := NewState()
	s.Upsert(makePod("default", "p", corev1.PodPending, 0, 1))
	s.Upsert(makePod("default", "p", corev1.PodRunning, 1, 1))
	snap := s.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("want 1 pod after re-upsert, got %d", len(snap))
	}
	if snap[0].Phase != "Running" || snap[0].Ready != "1/1" {
		t.Errorf("unexpected snapshot: %+v", snap[0])
	}
}
