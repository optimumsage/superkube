package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

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
	toolswatch "k8s.io/client-go/tools/watch"
)

// WatchTableOpts configures a live table watch. Resource accepts kubectl-style
// short or plural names ("po", "pods", "deployments.apps", CRD names, etc.).
type WatchTableOpts struct {
	Resource      string
	Namespace     string
	AllNamespaces bool
	Selector      string
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
// triggers a debounced re-fetch + re-render. Returns when ctx is cancelled.
//
// Why this design: the watch event stream is only used as a "something
// changed" signal. The actual rows always come from a fresh server-rendered
// Table LIST, which means we never have to merge Table-shaped watch events
// (an apiserver feature whose support across CRD versions is uneven). The
// trade-off is one extra GET per debounced burst, which the 80ms window keeps
// trivial for any realistic frame rate.
func (l Loader) WatchTable(ctx context.Context, opts WatchTableOpts, render func(TableFrame)) error {
	cfg, err := l.RESTConfig()
	if err != nil {
		return err
	}

	mapper, err := newRESTMapper(cfg)
	if err != nil {
		return err
	}
	gvr, namespaced, err := resolveGVR(mapper, opts.Resource)
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

	// fetch performs a Table-shaped LIST and renders one frame. Returns the
	// resource version so the initial call can seed the watcher.
	fetch := func() (string, error) {
		req := tableClient.Get().Resource(gvr.Resource)
		if namespaced && ns != "" {
			req = req.Namespace(ns)
		}
		if opts.Selector != "" {
			req = req.Param("labelSelector", opts.Selector)
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
		render(tableToFrame(&tbl, opts.AllNamespaces))
		return tbl.ResourceVersion, nil
	}

	initialRV, err := fetch()
	if err != nil {
		return err
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	watcher := watcherFunc(func(o metav1.ListOptions) (apiwatch.Interface, error) {
		if opts.Selector != "" {
			o.LabelSelector = opts.Selector
		}
		if namespaced && !opts.AllNamespaces {
			return dyn.Resource(gvr).Namespace(ns).Watch(ctx, o)
		}
		return dyn.Resource(gvr).Watch(ctx, o)
	})

	rw, err := toolswatch.NewRetryWatcher(initialRV, watcher)
	if err != nil {
		return err
	}
	defer rw.Stop()

	debounce := opts.DebounceWindow
	if debounce <= 0 {
		debounce = 80 * time.Millisecond
	}
	var (
		mu    sync.Mutex
		timer *time.Timer
	)
	schedule := func() {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(debounce, func() {
			// Best-effort: a refresh failure shouldn't tear down the loop. The
			// next event will trigger another fetch attempt.
			_, _ = fetch()
		})
	}

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
				schedule()
			}
		}
	}
}

type watcherFunc func(metav1.ListOptions) (apiwatch.Interface, error)

func (f watcherFunc) Watch(opts metav1.ListOptions) (apiwatch.Interface, error) {
	return f(opts)
}

func newRESTMapper(cfg *rest.Config) (meta.RESTMapper, error) {
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return restmapper.NewDeferredDiscoveryRESTMapper(memorycache.NewMemCacheClient(disco)), nil
}

// resolveGVR maps a kubectl-style resource string to a full GVR + scope. We
// take the first match the discovery mapper returns; ambiguous short names
// (e.g. "po" → pods, "events" → core+events.k8s.io) prefer the legacy core
// group, matching kubectl's behavior.
func resolveGVR(mapper meta.RESTMapper, resource string) (schema.GroupVersionResource, bool, error) {
	gvr, err := mapper.ResourceFor(schema.GroupVersionResource{Resource: resource})
	if err != nil {
		return schema.GroupVersionResource{}, false, err
	}
	gvk, err := mapper.KindFor(gvr)
	if err != nil {
		return gvr, false, err
	}
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
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
func tableToFrame(tbl *metav1.Table, allNS bool) TableFrame {
	headers := make([]string, 0, len(tbl.ColumnDefinitions)+1)
	if allNS {
		headers = append(headers, "NAMESPACE")
	}
	for _, c := range tbl.ColumnDefinitions {
		headers = append(headers, c.Name)
	}
	rows := make([][]string, 0, len(tbl.Rows))
	for _, r := range tbl.Rows {
		cells := make([]string, 0, len(r.Cells)+1)
		if allNS {
			cells = append(cells, namespaceFromRowObject(r.Object.Raw))
		}
		for _, c := range r.Cells {
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
