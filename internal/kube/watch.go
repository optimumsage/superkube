package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apiwatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	memorycache "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/cache"
	toolswatch "k8s.io/client-go/tools/watch"
)

// WatchTableOpts configures a live table watch. Resource accepts kubectl-style
// short or plural names ("po", "pods", "deployments.apps", CRD names, etc.).
type WatchTableOpts struct {
	Resource      string
	Name          string // optional: watch a single object (kubectl `get pod foo -w`)
	Namespace     string
	AllNamespaces bool
	Selector      string
	FieldSelector string
	// Wide includes the priority>0 columns that kubectl only prints with -o wide.
	Wide bool
	// DebounceWindow caps the redraw rate. Default 80ms if zero.
	DebounceWindow time.Duration
}

// TableFrame is the rendered shape of a single Table snapshot. The cli layer
// consumes it to draw the terminal frame.
type TableFrame struct {
	Headers []string
	Rows    [][]string
}

// WatchTable performs an initial Table-shaped LIST and renders the result via
// the callback, then opens a dynamic watch on the same resource. Each change
// triggers a debounced re-fetch + re-render. Returns when ctx is cancelled, or
// with an error if the watch cannot continue (e.g. RBAC forbids it).
//
// Why this design: the watch event stream is only used as a "something
// changed" signal. The actual rows always come from a fresh server-rendered
// Table LIST, which means we never have to merge Table-shaped watch events
// (an apiserver feature whose support across CRD versions is uneven). The
// trade-off is one extra GET per debounced burst, which the 80ms window keeps
// trivial for any realistic frame rate.
//
// Renders are serialized: a single goroutine owns refreshes and the event loop
// only pokes it through a 1-slot channel. A LIST slower than the debounce
// window therefore coalesces further events instead of overlapping renders
// (which would race on the caller's cursor-up bookkeeping and garble frames).
//
// When the watch ends without ctx being cancelled — typically HTTP 410 because
// the resourceVersion aged out — we re-LIST and start a new watch rather than
// exiting silently.
func (l Loader) WatchTable(ctx context.Context, opts WatchTableOpts, render func(TableFrame)) error {
	cfg, err := l.RESTConfig()
	if err != nil {
		return err
	}

	mapper, err := newRESTMapper(cfg)
	if err != nil {
		return err
	}
	gvr, namespaced, err := resolveGVR(ctx, mapper, opts.Resource)
	if err != nil {
		return fmt.Errorf("resolve resource %q: %w", opts.Resource, err)
	}

	ns := opts.Namespace
	if !opts.AllNamespaces && ns == "" && namespaced {
		if cur, _ := l.CurrentNamespace(); cur != "" {
			ns = cur
		}
	}
	if opts.AllNamespaces {
		ns = ""
	}

	tableClient, err := newTableRESTClient(cfg, gvr.GroupVersion())
	if err != nil {
		return err
	}

	fieldSel := opts.FieldSelector
	if opts.Name != "" {
		// kubectl does the same for a named watch: a metadata.name field
		// selector works for both LIST and WATCH and needs no extra code path.
		nameSel := "metadata.name=" + opts.Name
		if fieldSel != "" {
			fieldSel = nameSel + "," + fieldSel
		} else {
			fieldSel = nameSel
		}
	}

	// fetch performs a Table-shaped LIST and renders one frame. Returns the
	// resource version so the initial call can seed the watcher. fetchMu keeps
	// the initial/re-list render and the refresher's renders from overlapping.
	var fetchMu sync.Mutex
	fetch := func(ctx context.Context) (string, error) {
		fetchMu.Lock()
		defer fetchMu.Unlock()
		req := tableClient.Get().Resource(gvr.Resource)
		if namespaced && ns != "" {
			req = req.Namespace(ns)
		}
		if opts.Selector != "" {
			req = req.Param("labelSelector", opts.Selector)
		}
		if fieldSel != "" {
			req = req.Param("fieldSelector", fieldSel)
		}
		// Accept header MUST use "v=v1" (not "v=1") — that's the meta.k8s.io
		// API version. With "v=1" the apiserver silently ignores the Table
		// preference and falls through to returning a regular PodList, which
		// then unmarshals into an empty metav1.Table (no columns, no rows).
		// This matches what kubectl sends.
		req = req.SetHeader("Accept",
			"application/json;as=Table;v=v1;g=meta.k8s.io,"+
				"application/json;as=Table;v=v1beta1;g=meta.k8s.io,"+
				"application/json")
		raw, err := req.DoRaw(ctx)
		if err != nil {
			return "", err
		}
		var tbl metav1.Table
		if err := json.Unmarshal(raw, &tbl); err != nil {
			return "", fmt.Errorf("decode table: %w", err)
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		render(tableToFrame(&tbl, opts.AllNamespaces, opts.Wide))
		return tbl.ResourceVersion, nil
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	watcher := watcherFunc(func(wctx context.Context, o metav1.ListOptions) (apiwatch.Interface, error) {
		if opts.Selector != "" {
			o.LabelSelector = opts.Selector
		}
		if fieldSel != "" {
			o.FieldSelector = fieldSel
		}
		if namespaced && !opts.AllNamespaces {
			return dyn.Resource(gvr).Namespace(ns).Watch(wctx, o)
		}
		return dyn.Resource(gvr).Watch(wctx, o)
	})

	debounce := opts.DebounceWindow
	if debounce <= 0 {
		debounce = 80 * time.Millisecond
	}

	kick, stopRefresher := startRefresher(ctx, debounce, func(rctx context.Context) {
		// Best-effort: a refresh failure shouldn't tear down the loop. The
		// next event will trigger another fetch attempt.
		_, _ = fetch(rctx)
	})
	defer stopRefresher()

	backoff := time.Second
	for {
		rv, err := fetch(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		rw, err := toolswatch.NewRetryWatcherWithContext(ctx, rv, watcher)
		if err != nil {
			// Empty/"0" resourceVersion (some aggregated APIs): nothing to
			// resume from.
			return err
		}
		fatal := pumpWatch(ctx, rw, kick)
		rw.Stop()
		if ctx.Err() != nil {
			return nil
		}
		if fatal != nil {
			return fatal
		}
		// Watch ended on its own (410 Gone / resourceVersion too old, server
		// closed it). Re-list and watch again, backing off so a flapping
		// apiserver doesn't make us spin.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

// startRefresher runs do on a single goroutine, at most once per debounce
// window, whenever kick has been called since the last run. Because there is
// exactly one goroutine, runs can never overlap: kicks that arrive while do is
// in flight coalesce into one follow-up run. stop cancels and waits for the
// goroutine, so nothing renders after it returns.
func startRefresher(ctx context.Context, debounce time.Duration, do func(context.Context)) (kick func(), stop func()) {
	rctx, cancel := context.WithCancel(ctx)
	refresh := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-rctx.Done():
				return
			case <-refresh:
			}
			select {
			case <-rctx.Done():
				return
			case <-time.After(debounce):
			}
			// Drop kicks that arrived during the debounce: the run below
			// already reflects them.
			select {
			case <-refresh:
			default:
			}
			do(rctx)
		}
	}()
	return func() {
			select {
			case refresh <- struct{}{}:
			default:
			}
		}, func() {
			cancel()
			<-done
		}
}

// pumpWatch drains rw until its channel closes, calling kick on each change.
// It returns a non-nil error for failures a re-list cannot fix
// (Unauthorized/Forbidden). A nil return means "watch ended; re-list".
func pumpWatch(ctx context.Context, rw *toolswatch.RetryWatcher, kick func()) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-rw.ResultChan():
			if !ok {
				return nil
			}
			switch ev.Type {
			case apiwatch.Added, apiwatch.Modified, apiwatch.Deleted, apiwatch.Bookmark:
				kick()
			case apiwatch.Error:
				err := apierrors.FromObject(ev.Object)
				if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
					return err
				}
				// Anything else (410 Gone, 5xx): the channel closes next and
				// the caller re-lists.
			}
		}
	}
}

// watcherFunc adapts a closure to cache.WatcherWithContext.
type watcherFunc func(context.Context, metav1.ListOptions) (apiwatch.Interface, error)

func (f watcherFunc) WatchWithContext(ctx context.Context, o metav1.ListOptions) (apiwatch.Interface, error) {
	return f(ctx, o)
}

var _ cache.WatcherWithContext = watcherFunc(nil)

// newRESTMapper builds a discovery-backed mapper whose lookups take a context,
// so Ctrl-C during a slow discovery round-trip aborts promptly.
func newRESTMapper(cfg *rest.Config) (meta.RESTMapperWithContext, error) {
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return restmapper.NewDeferredDiscoveryRESTMapperWithContext(memorycache.NewMemCacheClientWithContext(disco)), nil
}

// resolveGVR maps a kubectl-style resource string to a full GVR + scope. We
// take the first match the discovery mapper returns; ambiguous short names
// (e.g. "po" → pods, "events" → core+events.k8s.io) prefer the legacy core
// group, matching kubectl's behavior.
func resolveGVR(ctx context.Context, mapper meta.RESTMapperWithContext, resource string) (schema.GroupVersionResource, bool, error) {
	gvr, err := mapper.ResourceForWithContext(ctx, schema.GroupVersionResource{Resource: resource})
	if err != nil {
		return schema.GroupVersionResource{}, false, err
	}
	gvk, err := mapper.KindForWithContext(ctx, gvr)
	if err != nil {
		return gvr, false, err
	}
	mapping, err := mapper.RESTMappingWithContext(ctx, gvk.GroupKind(), gvk.Version)
	if err != nil {
		return gvr, false, err
	}
	return gvr, mapping.Scope.Name() == meta.RESTScopeNameNamespace, nil
}

// newTableRESTClient builds a REST client wired to the resource's GroupVersion
// so callers can request Table-shaped responses for List + Watch directly.
func newTableRESTClient(cfg *rest.Config, gv schema.GroupVersion) (rest.Interface, error) {
	c := rest.CopyConfig(cfg)
	c.GroupVersion = &gv
	c.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	if gv.Group == "" {
		c.APIPath = "/api"
	} else {
		c.APIPath = "/apis"
	}
	return rest.RESTClientFor(c)
}

// tableToFrame flattens a metav1.Table into stringly headers + rows. When
// allNS is true the synthesized NAMESPACE column is prepended (Table responses
// don't include it; we extract from each row's embedded object metadata).
//
// Columns with Priority > 0 are the "wide" ones kubectl only prints under
// -o wide; they are dropped unless wide is set.
func tableToFrame(tbl *metav1.Table, allNS, wide bool) TableFrame {
	headers := make([]string, 0, len(tbl.ColumnDefinitions)+1)
	if allNS {
		headers = append(headers, "NAMESPACE")
	}
	keep := make([]bool, len(tbl.ColumnDefinitions))
	for i, c := range tbl.ColumnDefinitions {
		keep[i] = wide || c.Priority == 0
		if keep[i] {
			headers = append(headers, c.Name)
		}
	}
	rows := make([][]string, 0, len(tbl.Rows))
	for _, r := range tbl.Rows {
		cells := make([]string, 0, len(r.Cells)+1)
		if allNS {
			cells = append(cells, namespaceFromRowObject(r.Object.Raw))
		}
		for i, c := range r.Cells {
			if i < len(keep) && !keep[i] {
				continue
			}
			cells = append(cells, fmt.Sprint(c))
		}
		rows = append(rows, cells)
	}
	return TableFrame{Headers: headers, Rows: rows}
}

func namespaceFromRowObject(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var probe struct {
		Metadata struct {
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Metadata.Namespace
}
