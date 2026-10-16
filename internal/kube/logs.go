package kube

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	Selector      string    // raw label selector, e.g. "app=web,tier=frontend"
	Workload      string    // "deploy/foo" form
	Container     string    // empty = each pod's default container
	AllContainers bool      // stream every container of every pod (like kubectl --all-containers)
	Warn          io.Writer // transient-problem notices (default os.Stderr)
	TailLines     int64     // <= 0 = no limit
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

// openLogStream opens one container's log stream. Injected so tests can
// simulate transient failures (the fake clientset's GetLogs cannot fail).
type openLogStream func(ctx context.Context, pod, container string, tail *int64) (io.ReadCloser, error)

// Retry policy for opening a log stream. A container that is still starting
// answers "waiting to start"; a short bounded retry rides that out, while
// anything longer is picked up by the pod watch when the pod changes state.
var (
	logOpenAttempts   = 4
	logOpenRetryDelay = time.Second
)

func tailMulti(ctx context.Context, cs kubernetes.Interface, opts MultiLogOpts, w io.Writer, prefix LinePrefixer) error {
	open := func(ctx context.Context, pod, container string, tail *int64) (io.ReadCloser, error) {
		return cs.CoreV1().Pods(opts.Namespace).GetLogs(pod, &corev1.PodLogOptions{
			Follow:    opts.Follow,
			Container: container,
			TailLines: tail,
		}).Stream(ctx)
	}
	return tailMultiWith(ctx, cs, opts, w, prefix, open)
}

func tailMultiWith(ctx context.Context, cs kubernetes.Interface, opts MultiLogOpts, w io.Writer, prefix LinePrefixer, open openLogStream) error {
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
	warnOut := opts.Warn
	if warnOut == nil {
		warnOut = os.Stderr
	}
	var writeMu sync.Mutex
	say := func(name string, i int, line string) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_, _ = fmt.Fprintln(w, prefix(name, i)+line)
	}

	// Streams run under sctx so a fatal watch error can stop them all.
	sctx, cancelStreams := context.WithCancel(ctx)
	defer cancelStreams()

	var (
		wg      sync.WaitGroup
		claimMu sync.Mutex
		claimed = map[string]bool{} // pod UID + container
	)
	claim := func(key string) bool {
		claimMu.Lock()
		defer claimMu.Unlock()
		if claimed[key] {
			return false
		}
		claimed[key] = true
		return true
	}
	release := func(key string) {
		claimMu.Lock()
		delete(claimed, key)
		claimMu.Unlock()
	}

	streamOne := func(pod *corev1.Pod, c, name string, tail *int64) {
		defer wg.Done()
		key := string(pod.UID) + "/" + pod.Name + "/" + c
		for attempt := 1; ; attempt++ {
			stream, err := open(sctx, pod.Name, c, tail)
			if err == nil {
				defer func() { _ = stream.Close() }()
				if err := emitLines(stream, func(i int, line string) { say(name, i, line) }); err != nil && sctx.Err() == nil {
					say(name, 0, "(log stream ended with error: "+err.Error()+")")
				}
				return
			}
			if sctx.Err() != nil {
				return
			}
			if attempt >= logOpenAttempts || apierrors.IsForbidden(err) || apierrors.IsNotFound(err) {
				// Give the claim back so a later pod update (e.g. the container
				// finally starting) can try again via the watch.
				release(key)
				say(name, 0, "(error opening log stream: "+err.Error()+")")
				return
			}
			select {
			case <-sctx.Done():
				return
			case <-time.After(time.Duration(attempt) * logOpenRetryDelay):
			}
		}
	}

	// startPod streams every not-yet-claimed, started container of pod. A
	// container is claimed only when we are about to open it, so a pod that was
	// Pending (or a container still waiting) at startup is picked up by a later
	// watch event instead of being skipped forever.
	startPod := func(pod *corev1.Pod, tail *int64, skipUnstarted bool) {
		containers := containersToTail(pod, opts)
		for _, c := range containers {
			if skipUnstarted && !containerStarted(pod, c) {
				continue
			}
			if !claim(string(pod.UID) + "/" + pod.Name + "/" + c) {
				continue
			}
			name := pod.Name
			if len(containers) > 1 {
				name = pod.Name + "/" + c
			}
			wg.Add(1)
			go streamOne(pod, c, name, tail)
		}
	}

	for i := range pods.Items {
		startPod(&pods.Items[i], tailLinesOrNil(opts.TailLines), opts.Follow)
	}

	var watchErr error
	if opts.Follow {
		watchErr = watchNewPods(sctx, cs, ns, selector, pods.ResourceVersion, func(p *corev1.Pod) {
			// A pod (or container) that started after we began: its whole log
			// is new, so don't apply the tail limit.
			startPod(p, nil, true)
		}, rateLimitedWarn(warnOut, 10*time.Second))
		if watchErr != nil {
			cancelStreams()
		}
	}
	wg.Wait()
	return watchErr
}

// containerStarted reports whether container c of pod has produced (or can
// produce) logs: it is running or has terminated. Without a status entry we
// fall back to the pod phase.
func containerStarted(pod *corev1.Pod, c string) bool {
	for _, list := range [][]corev1.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses} {
		for _, st := range list {
			if st.Name == c {
				return st.State.Running != nil || st.State.Terminated != nil
			}
		}
	}
	return pod.Status.Phase != corev1.PodPending && pod.Status.Phase != corev1.PodUnknown
}

// rateLimitedWarn returns a func that writes at most one line to w per
// interval, so a flapping apiserver doesn't flood the log stream.
func rateLimitedWarn(w io.Writer, interval time.Duration) func(string) {
	var mu sync.Mutex
	var last time.Time
	return func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		if !last.IsZero() && time.Since(last) < interval {
			return
		}
		last = time.Now()
		_, _ = fmt.Fprintln(w, "warning: "+msg)
	}
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

// watchNewPods invokes onPod for every Added/Modified pod on the selector until
// ctx is cancelled (returns nil) or the watch hits a permanent authorization
// failure (returns that error). onPod decides what to do with the pod, so a
// pod that was Pending when first seen is handed over again once it changes.
//
// Transient failures — a failed watch call, an Error event, the stream closing
// — are reported through warn and retried after a re-list that resyncs the
// resourceVersion (which also covers 410 Gone).
func watchNewPods(ctx context.Context, cs kubernetes.Interface, ns, selector, rv string, onPod func(*corev1.Pod), warn func(string)) error {
	for ctx.Err() == nil {
		w, err := cs.CoreV1().Pods(ns).Watch(ctx, metav1.ListOptions{LabelSelector: selector, ResourceVersion: rv})
		if err != nil {
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
				return fmt.Errorf("watching pods: %w", err)
			}
			warn("pod watch failed, retrying: " + err.Error())
		} else {
			if ferr := pumpPods(ctx, w, &rv, onPod, warn); ferr != nil {
				w.Stop()
				return ferr
			}
			w.Stop()
		}
		if ctx.Err() != nil {
			return nil
		}
		// Watch ended or failed: back off, then re-list to resync the RV.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
		if list, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector}); err == nil {
			rv = list.ResourceVersion
			for i := range list.Items {
				onPod(&list.Items[i])
			}
		} else if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			return fmt.Errorf("listing pods: %w", err)
		}
	}
	return nil
}

// pumpPods drains one watch. It returns a non-nil error only for permanent
// authorization failures; any other end (channel closed, ctx done, transient
// Error event) returns nil so the caller re-lists.
func pumpPods(ctx context.Context, w apiwatch.Interface, rv *string, onPod func(*corev1.Pod), warn func(string)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.ResultChan():
			if !ok {
				return nil
			}
			switch ev.Type {
			case apiwatch.Error:
				err := apierrors.FromObject(ev.Object)
				if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
					return fmt.Errorf("watching pods: %w", err)
				}
				if !apierrors.IsGone(err) && !apierrors.IsResourceExpired(err) {
					warn("pod watch error: " + err.Error())
				}
				return nil
			case apiwatch.Added, apiwatch.Modified:
				if p, isPod := ev.Object.(*corev1.Pod); isPod {
					onPod(p)
					*rv = p.ResourceVersion
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
