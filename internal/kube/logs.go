package kube

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	apiwatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

// MultiLogOpts configures a multi-pod tail. Either Selector or Workload must
// be set. Workload uses a "deploy/foo" / "statefulset/bar" / "daemonset/baz"
// shorthand; we resolve it to a label selector via the workload's spec.
type MultiLogOpts struct {
	Namespace     string
	Selector      string // raw label selector, e.g. "app=web,tier=frontend"
	Workload      string // "deploy/foo" form
	Container     string // empty = each pod's default container
	AllContainers bool   // stream every container of every pod (like kubectl --all-containers)
	TailLines     int64  // <= 0 = no limit
	Follow        bool
}

// defaultContainerAnnotation is the annotation kubectl itself consults to pick
// the container for `logs`/`exec` when a pod has several.
const defaultContainerAnnotation = "kubectl.kubernetes.io/default-container"

// LinePrefixer formats the per-line prefix for pod podName, lineIndex zero-
// based. Override to colorize per pod; pass nil for the default "[<pod>] ".
type LinePrefixer func(podName string, lineIndex int) string

// TailMulti expands opts into a set of pods and streams each pod's logs onto
// w. Stream goroutines run concurrently; writes to w are serialized so lines
// from different pods don't interleave mid-line on the terminal.
//
// With Follow, a pod watch on the selector picks up pods created later (for
// example during a rollout) and streams them from their start; in that mode
// TailMulti runs until ctx is cancelled. Without Follow it returns once every
// stream has completed.
func (l Loader) TailMulti(ctx context.Context, opts MultiLogOpts, w io.Writer, prefix LinePrefixer) error {
	cs, err := l.Clientset()
	if err != nil {
		return err
	}
	if opts.Namespace == "" {
		opts.Namespace, _ = l.CurrentNamespace()
	}
	return tailMulti(ctx, cs, opts, w, prefix)
}

func tailMulti(ctx context.Context, cs kubernetes.Interface, opts MultiLogOpts, w io.Writer, prefix LinePrefixer) error {
	ns := opts.Namespace

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
	say := func(name string, i int, line string) {
		writeMu.Lock()
		defer writeMu.Unlock()
		fmt.Fprintln(w, prefix(name, i)+line)
	}

	var (
		wg      sync.WaitGroup
		seenMu  sync.Mutex
		started = map[string]bool{}
	)
	startPod := func(pod *corev1.Pod, tail *int64) {
		seenMu.Lock()
		if started[pod.Name+"/"+string(pod.UID)] {
			seenMu.Unlock()
			return
		}
		started[pod.Name+"/"+string(pod.UID)] = true
		seenMu.Unlock()

		containers := containersToTail(pod, opts)
		for _, c := range containers {
			name := pod.Name
			if len(containers) > 1 {
				name = pod.Name + "/" + c
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := cs.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
					Follow:    opts.Follow,
					Container: c,
					TailLines: tail,
				})
				stream, err := req.Stream(ctx)
				if err != nil {
					say(name, 0, "(error opening log stream: "+err.Error()+")")
					return
				}
				defer stream.Close()
				if err := emitLines(stream, func(i int, line string) { say(name, i, line) }); err != nil && ctx.Err() == nil {
					say(name, 0, "(log stream ended with error: "+err.Error()+")")
				}
			}()
		}
	}

	for i := range pods.Items {
		startPod(&pods.Items[i], tailLinesOrNil(opts.TailLines))
	}

	if opts.Follow {
		watchNewPods(ctx, cs, ns, selector, pods.ResourceVersion, func(p *corev1.Pod) {
			// A pod that appeared after we started: its whole log is new, so
			// don't apply the tail limit.
			startPod(p, nil)
		})
	}
	wg.Wait()
	return nil
}

// containersToTail picks which containers of pod to stream. An explicit
// Container wins, then AllContainers (init containers included, like kubectl),
// else the pod's default container: the kubectl default-container annotation
// if it names a real container, otherwise the first regular container. Passing
// an empty container to the API fails for multi-container pods, which is why
// we always resolve one.
func containersToTail(pod *corev1.Pod, opts MultiLogOpts) []string {
	if opts.Container != "" {
		return []string{opts.Container}
	}
	if opts.AllContainers {
		var out []string
		for _, c := range pod.Spec.InitContainers {
			out = append(out, c.Name)
		}
		for _, c := range pod.Spec.Containers {
			out = append(out, c.Name)
		}
		return out
	}
	if def := pod.Annotations[defaultContainerAnnotation]; def != "" {
		for _, c := range pod.Spec.Containers {
			if c.Name == def {
				return []string{def}
			}
		}
	}
	if len(pod.Spec.Containers) > 0 {
		return []string{pod.Spec.Containers[0].Name}
	}
	return []string{""}
}

// emitLines reads r line by line with no length cap (bufio.Scanner would fail
// the whole stream on a line past its buffer, silently losing the pod's
// remaining output) and returns any non-EOF read error.
func emitLines(r io.Reader, emit func(i int, line string)) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for i := 0; ; i++ {
		line, err := br.ReadString('\n')
		if line != "" {
			emit(i, strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// watchNewPods invokes onPod for every Running (or already finished) pod that
// shows up on the selector, until ctx is cancelled. It re-lists and re-watches
// if the watch drops, so a long-running tail survives apiserver restarts and
// 410s. Pods still Pending are skipped until a later Modified event shows them
// running, since their log stream would fail with "container is waiting".
func watchNewPods(ctx context.Context, cs kubernetes.Interface, ns, selector, rv string, onPod func(*corev1.Pod)) {
	ready := func(p *corev1.Pod) bool {
		return p.Status.Phase != corev1.PodPending && p.Status.Phase != corev1.PodUnknown
	}
	for ctx.Err() == nil {
		w, err := cs.CoreV1().Pods(ns).Watch(ctx, metav1.ListOptions{LabelSelector: selector, ResourceVersion: rv})
		if err == nil {
		events:
			for {
				select {
				case <-ctx.Done():
					break events
				case ev, ok := <-w.ResultChan():
					if !ok {
						break events
					}
					p, isPod := ev.Object.(*corev1.Pod)
					if !isPod {
						continue
					}
					if (ev.Type == apiwatch.Added || ev.Type == apiwatch.Modified) && ready(p) {
						onPod(p)
					}
					rv = p.ResourceVersion
				}
			}
			w.Stop()
		}
		if ctx.Err() != nil {
			return
		}
		// Watch ended or failed: back off, then re-list to resync the RV.
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
		if list, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector}); err == nil {
			rv = list.ResourceVersion
			for i := range list.Items {
				if ready(&list.Items[i]) {
					onPod(&list.Items[i])
				}
			}
		}
	}
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
func resolveWorkloadSelector(ctx context.Context, cs kubernetes.Interface, ns, workload string) (string, error) {
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
