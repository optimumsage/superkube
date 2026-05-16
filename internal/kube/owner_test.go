package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func boolPtr(b bool) *bool { return &b }

// renderChainWithCS exercises OwnerChain's algorithm using an injected
// clientset, so tests don't need a kubeconfig. Mirrors the production loop
// exactly so any divergence is caught here.
func renderChainWithCS(t *testing.T, objs ...runtime.Object) string {
	t.Helper()
	cs := fake.NewSimpleClientset(objs...)
	var pod *corev1.Pod
	for _, o := range objs {
		if p, ok := o.(*corev1.Pod); ok {
			pod = p
			break
		}
	}
	if pod == nil {
		t.Fatal("no Pod in test inputs")
	}
	parts := []string{"pod/" + pod.Name}
	kind, name := controller(pod.OwnerReferences)
	for kind != "" {
		parts = append(parts, shortKind(kind)+"/"+name)
		next, nextName, ok := lookupOwner(context.Background(), cs, pod.Namespace, kind, name)
		if !ok {
			break
		}
		kind, name = next, nextName
	}
	return strings.Join(parts, " ← ")
}

func TestOwnerChain(t *testing.T) {
	cases := []struct {
		name string
		objs []runtime.Object
		want string
	}{
		{
			name: "pod -> rs -> deploy",
			objs: []runtime.Object{
				podOwnedBy("web-abc-1", "default", "ReplicaSet", "web-abc"),
				rsOwnedBy("web-abc", "default", "Deployment", "web"),
				deploy("web", "default"),
			},
			want: "pod/web-abc-1 ← rs/web-abc ← deploy/web",
		},
		{
			name: "pod -> sts",
			objs: []runtime.Object{
				podOwnedBy("db-0", "default", "StatefulSet", "db"),
				sts("db", "default"),
			},
			want: "pod/db-0 ← sts/db",
		},
		{
			name: "bare pod",
			objs: []runtime.Object{
				&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "naked", Namespace: "default"}},
			},
			want: "pod/naked",
		},
		{
			name: "owner missing — chain stops with what we have",
			objs: []runtime.Object{
				podOwnedBy("orphan-1", "default", "ReplicaSet", "missing"),
			},
			want: "pod/orphan-1 ← rs/missing",
		},
		{
			name: "pod -> job -> cronjob",
			objs: []runtime.Object{
				podOwnedBy("cron-1-xyz", "default", "Job", "cron-1"),
				jobOwnedBy("cron-1", "default", "CronJob", "cron"),
			},
			want: "pod/cron-1-xyz ← job/cron-1 ← cj/cron",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderChainWithCS(t, tc.objs...)
			if got != tc.want {
				t.Errorf("chain mismatch\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestSiblingPodsForReplicaSet exercises controllerSelector + the pod listing
// against a ReplicaSet's matchLabels. The fake clientset honors LabelSelector
// queries, so this validates the end-to-end shape without a real apiserver.
func TestSiblingPodsForReplicaSet(t *testing.T) {
	lbls := map[string]string{"app": "web", "pod-template-hash": "abc"}
	cs := fake.NewSimpleClientset(
		podOwnedByReady("web-abc-1", "default", "ReplicaSet", "web-abc", lbls, 2*time.Minute),
		podOwnedByReady("web-abc-2", "default", "ReplicaSet", "web-abc", lbls, 30*time.Second),
		rsWithSelector("web-abc", "default", lbls),
	)
	pod, err := cs.CoreV1().Pods("default").Get(context.Background(), "web-abc-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	kind, name := controller(pod.OwnerReferences)
	sel, err := controllerSelector(context.Background(), cs, "default", kind, name)
	if err != nil {
		t.Fatalf("controllerSelector: %v", err)
	}
	if sel == "" {
		t.Fatal("expected non-empty selector for an RS with matchLabels")
	}
	pods, err := cs.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 2 {
		t.Fatalf("want 2 sibling pods, got %d (sel=%q)", len(pods.Items), sel)
	}
	for _, p := range pods.Items {
		if readyString(&p) != "2/2" {
			t.Errorf("%s: want ready 2/2, got %s", p.Name, readyString(&p))
		}
	}
}

func TestHumanAge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		in   time.Time
		want string
	}{
		{now.Add(-15 * time.Second), "15s"},
		{now.Add(-3 * time.Minute), "3m"},
		{now.Add(-2 * time.Hour), "2h"},
		{now.Add(-50 * time.Hour), "2d"},
	}
	for _, tc := range cases {
		got := humanAge(tc.in)
		if got != tc.want {
			t.Errorf("humanAge(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if humanAge(time.Time{}) != "?" {
		t.Error("humanAge(zero) should be \"?\"")
	}
}

// --- builders ---

func podOwnedBy(name, ns, ownerKind, ownerName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{
				{Kind: ownerKind, Name: ownerName, Controller: boolPtr(true)},
			},
		},
	}
}

func podOwnedByReady(name, ns, ownerKind, ownerName string, lbls map[string]string, age time.Duration) *corev1.Pod {
	p := podOwnedBy(name, ns, ownerKind, ownerName)
	p.ObjectMeta.Labels = lbls
	p.ObjectMeta.CreationTimestamp = metav1.NewTime(time.Now().Add(-age))
	p.Status.Phase = corev1.PodRunning
	p.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "a", Ready: true},
		{Name: "b", Ready: true},
	}
	return p
}

func rsOwnedBy(name, ns, ownerKind, ownerName string) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{
				{Kind: ownerKind, Name: ownerName, Controller: boolPtr(true)},
			},
		},
	}
}

func rsWithSelector(name, ns string, lbls map[string]string) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: lbls},
		},
	}
}

func deploy(name, ns string) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

func sts(name, ns string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

func jobOwnedBy(name, ns, ownerKind, ownerName string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{
				{Kind: ownerKind, Name: ownerName, Controller: boolPtr(true)},
			},
		},
	}
}
