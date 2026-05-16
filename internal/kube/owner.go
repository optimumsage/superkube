package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// OwnerChain walks pod.OwnerReferences upward and returns a chain like
// "pod/foo ← rs/foo-abc123 ← deploy/foo". Best-effort: any lookup error
// returns what's been built so far — diagnose must not fail because of a
// permissions glitch on an intermediate object.
func (l Loader) OwnerChain(ctx context.Context, ns, podName string) (string, error) {
	cs, err := l.Clientset()
	if err != nil {
		return "", err
	}
	pod, err := cs.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	parts := []string{"pod/" + pod.Name}
	kind, name := controller(pod.OwnerReferences)
	for kind != "" {
		parts = append(parts, shortKind(kind)+"/"+name)
		next, nextName, ok := lookupOwner(ctx, cs, ns, kind, name)
		if !ok {
			break
		}
		kind, name = next, nextName
	}
	return strings.Join(parts, " ← "), nil
}

// SiblingPods returns one-line summaries of pods sharing podName's immediate
// controller. Empty string if the pod has no controller. Format per line:
// `<name>  <phase>  <ready>  <restarts>  <age>`.
func (l Loader) SiblingPods(ctx context.Context, ns, podName string) (string, error) {
	cs, err := l.Clientset()
	if err != nil {
		return "", err
	}
	pod, err := cs.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	kind, name := controller(pod.OwnerReferences)
	if kind == "" {
		return "", nil
	}
	sel, err := controllerSelector(ctx, cs, ns, kind, name)
	if err != nil || sel == "" {
		return "", err
	}
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return "", err
	}
	sort.Slice(pods.Items, func(i, j int) bool {
		return pods.Items[i].Name < pods.Items[j].Name
	})
	var b strings.Builder
	for _, p := range pods.Items {
		fmt.Fprintf(&b, "%s  %s  %s  %d  %s\n",
			p.Name, p.Status.Phase, readyString(&p), totalRestarts(&p), humanAge(p.CreationTimestamp.Time))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// controller picks the OwnerReference marked Controller=true. Returns ("","") if
// none. Pods only have a single controller at a time so we don't tiebreak.
func controller(refs []metav1.OwnerReference) (kind, name string) {
	for _, r := range refs {
		if r.Controller != nil && *r.Controller {
			return r.Kind, r.Name
		}
	}
	return "", ""
}

// lookupOwner fetches the named owner and returns its own controller (if any).
// The boolean is false when the object can't be resolved — caller stops the walk.
func lookupOwner(ctx context.Context, cs kubernetes.Interface, ns, kind, name string) (string, string, bool) {
	switch strings.ToLower(kind) {
	case "replicaset":
		obj, err := cs.AppsV1().ReplicaSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", "", false
		}
		k, n := controller(obj.OwnerReferences)
		return k, n, true
	case "deployment":
		obj, err := cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", "", false
		}
		k, n := controller(obj.OwnerReferences)
		return k, n, true
	case "statefulset":
		obj, err := cs.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", "", false
		}
		k, n := controller(obj.OwnerReferences)
		return k, n, true
	case "daemonset":
		obj, err := cs.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", "", false
		}
		k, n := controller(obj.OwnerReferences)
		return k, n, true
	case "job":
		obj, err := cs.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", "", false
		}
		k, n := controller(obj.OwnerReferences)
		return k, n, true
	case "cronjob":
		// CronJob is the top — it has no controller of its own.
		return "", "", true
	}
	return "", "", false
}

// controllerSelector returns the label selector for the immediate controller
// kind/name. Used to enumerate sibling pods. Empty string for kinds we don't
// recognize; the caller treats that as "no siblings".
func controllerSelector(ctx context.Context, cs kubernetes.Interface, ns, kind, name string) (string, error) {
	var sel *metav1.LabelSelector
	switch strings.ToLower(kind) {
	case "replicaset":
		obj, err := cs.AppsV1().ReplicaSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		sel = obj.Spec.Selector
	case "statefulset":
		obj, err := cs.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		sel = obj.Spec.Selector
	case "daemonset":
		obj, err := cs.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		sel = obj.Spec.Selector
	case "job":
		obj, err := cs.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		sel = obj.Spec.Selector
	default:
		return "", nil
	}
	if sel == nil {
		return "", nil
	}
	asMap, err := metav1.LabelSelectorAsMap(sel)
	if err != nil {
		return "", err
	}
	return labels.SelectorFromSet(asMap).String(), nil
}

// shortKind maps long kinds to the abbreviations users see in kubectl output.
// Unknown kinds pass through lowercased so the chain still reads naturally.
func shortKind(kind string) string {
	switch strings.ToLower(kind) {
	case "deployment":
		return "deploy"
	case "replicaset":
		return "rs"
	case "statefulset":
		return "sts"
	case "daemonset":
		return "ds"
	case "cronjob":
		return "cj"
	}
	return strings.ToLower(kind)
}

func readyString(p *corev1.Pod) string {
	ready, total := 0, len(p.Status.ContainerStatuses)
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, total)
}

func totalRestarts(p *corev1.Pod) int32 {
	var n int32
	for _, cs := range p.Status.ContainerStatuses {
		n += cs.RestartCount
	}
	return n
}

// humanAge renders a duration the way kubectl get does: 30s, 14m, 3h, 5d.
func humanAge(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
