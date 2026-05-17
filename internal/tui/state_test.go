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

func TestStateMultiKindIsolation(t *testing.T) {
	s := NewState()
	s.Upsert(makePod("default", "pod-a", corev1.PodRunning, 1, 1))
	s.UpsertRow(KindConfigMap, ConfigMapRow{Namespace: "default", Name: "cm-b", DataKeys: 3})
	s.UpsertRow(KindConfigMap, ConfigMapRow{Namespace: "default", Name: "cm-a", DataKeys: 1})
	s.UpsertRow(KindSecret, SecretRow{Namespace: "default", Name: "sec-x", Type: "Opaque", DataKeys: 2})
	s.UpsertRow(KindIngress, IngressRow{Namespace: "default", Name: "ing-y", Class: "nginx", Hosts: "example.com"})

	if got := len(s.SnapshotKind(KindPod)); got != 1 {
		t.Errorf("pods snapshot len = %d, want 1", got)
	}
	cms := s.SnapshotKind(KindConfigMap)
	if len(cms) != 2 {
		t.Fatalf("configmaps snapshot len = %d, want 2", len(cms))
	}
	// Sort order: namespace then name → cm-a before cm-b.
	if cms[0].GetName() != "cm-a" || cms[1].GetName() != "cm-b" {
		t.Errorf("expected cm-a, cm-b ordering, got %s, %s", cms[0].GetName(), cms[1].GetName())
	}
	if got := len(s.SnapshotKind(KindSecret)); got != 1 {
		t.Errorf("secrets snapshot len = %d, want 1", got)
	}
	if got := len(s.SnapshotKind(KindIngress)); got != 1 {
		t.Errorf("ingresses snapshot len = %d, want 1", got)
	}

	// Deleting one kind doesn't affect the others.
	s.DeleteRow(KindConfigMap, "default/cm-a")
	if got := len(s.SnapshotKind(KindConfigMap)); got != 1 {
		t.Errorf("after configmap delete, configmaps len = %d, want 1", got)
	}
	if got := len(s.SnapshotKind(KindPod)); got != 1 {
		t.Errorf("pod count changed after unrelated configmap delete: %d", got)
	}
}

func TestConfigMapRowCols(t *testing.T) {
	r := ConfigMapRow{Namespace: "n", Name: "c", DataKeys: 4}
	if got := r.Cols(); len(got) != 1 || got[0] != "4" {
		t.Errorf("ConfigMapRow.Cols() = %v, want [\"4\"]", got)
	}
}

func TestSecretRowCols(t *testing.T) {
	r := SecretRow{Namespace: "n", Name: "s", Type: "Opaque", DataKeys: 2}
	got := r.Cols()
	if len(got) != 2 || got[0] != "Opaque" || got[1] != "2" {
		t.Errorf("SecretRow.Cols() = %v, want [Opaque, 2]", got)
	}
}

func TestIngressRowCols(t *testing.T) {
	r := IngressRow{Namespace: "n", Name: "i", Class: "nginx", Hosts: "a.example.com,b.example.com"}
	got := r.Cols()
	if len(got) != 2 || got[0] != "nginx" || got[1] != "a.example.com,b.example.com" {
		t.Errorf("IngressRow.Cols() = %v", got)
	}
}
