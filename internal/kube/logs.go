package kube

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// MultiLogOpts configures a multi-pod tail. Either Selector or Workload must
// be set. Workload uses a "deploy/foo" / "statefulset/bar" / "daemonset/baz"
// shorthand; we resolve it to a label selector via the workload's spec.
type MultiLogOpts struct {
	Namespace string
	Selector  string // raw label selector, e.g. "app=web,tier=frontend"
	Workload  string // "deploy/foo" form
	Container string // empty = first container per pod
	TailLines int64  // <= 0 = no limit
	Follow    bool
}

// LinePrefixer formats the per-line prefix for pod podName, lineIndex zero-
// based. Override to colorize per pod; pass nil for the default "[<pod>] ".
type LinePrefixer func(podName string, lineIndex int) string

// TailMulti expands opts into a set of pods and streams each pod's logs onto
// w. Stream goroutines run concurrently; writes to w are serialized so lines
// from different pods don't interleave mid-line on the terminal.
//
// Returns when all streams complete or when ctx is cancelled.
func (l Loader) TailMulti(ctx context.Context, opts MultiLogOpts, w io.Writer, prefix LinePrefixer) error {
	cs, err := l.Clientset()
	if err != nil {
		return err
	}
	ns := opts.Namespace
	if ns == "" {
		ns, _ = l.CurrentNamespace()
	}

	selector := opts.Selector
	if opts.Workload != "" {
		s, err := resolveWorkloadSelector(ctx, cs, ns, opts.Workload)
		if err != nil {
			return err
		}
		selector = s
	}
	if selector == "" {
		return fmt.Errorf("TailMulti: need Selector or Workload")
	}

	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods match selector %q in namespace %q", selector, ns)
	}

	if prefix == nil {
		prefix = func(name string, _ int) string { return "[" + name + "] " }
	}
	var writeMu sync.Mutex
	emitFrom := func(podName string, r io.Reader) {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		i := 0
		for scanner.Scan() {
			line := scanner.Text()
			writeMu.Lock()
			fmt.Fprintln(w, prefix(podName, i)+line)
			writeMu.Unlock()
			i++
		}
	}

	var wg sync.WaitGroup
	for _, pod := range pods.Items {
		pod := pod
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := cs.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
				Follow:    opts.Follow,
				Container: opts.Container,
				TailLines: tailLinesOrNil(opts.TailLines),
			})
			stream, err := req.Stream(ctx)
			if err != nil {
				writeMu.Lock()
				fmt.Fprintln(w, prefix(pod.Name, 0)+"(error opening log stream: "+err.Error()+")")
				writeMu.Unlock()
				return
			}
			defer stream.Close()
			emitFrom(pod.Name, stream)
		}()
	}
	wg.Wait()
	return nil
}

func tailLinesOrNil(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}

// resolveWorkloadSelector turns "deploy/web" into a label selector string by
// looking up the workload's spec.selector.matchLabels. Supports deploy,
// statefulset, daemonset, and replicaset.
func resolveWorkloadSelector(ctx context.Context, cs *kubernetes.Clientset, ns, workload string) (string, error) {
	parts := strings.SplitN(workload, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("workload must be TYPE/NAME (got %q)", workload)
	}
	kind, name := strings.ToLower(parts[0]), parts[1]

	var sel *metav1.LabelSelector
	switch kind {
	case "deploy", "deployment", "deployments":
		obj, err := cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		sel = obj.Spec.Selector
	case "sts", "statefulset", "statefulsets":
		obj, err := cs.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		sel = obj.Spec.Selector
	case "ds", "daemonset", "daemonsets":
		obj, err := cs.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		sel = obj.Spec.Selector
	case "rs", "replicaset", "replicasets":
		obj, err := cs.AppsV1().ReplicaSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		sel = obj.Spec.Selector
	default:
		return "", fmt.Errorf("unsupported workload kind %q (try deploy, sts, ds, rs)", kind)
	}
	if sel == nil {
		return "", fmt.Errorf("%s has no selector", workload)
	}
	asMap, err := metav1.LabelSelectorAsMap(sel)
	if err != nil {
		return "", err
	}
	return labels.SelectorFromSet(asMap).String(), nil
}
