package tui

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apiwatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// StartInformers wires informers for Pods, ConfigMaps, Secrets, and Ingresses,
// each writing into state. When ns is empty all namespaces are watched.
// Returns an idempotent stop func that cancels the informers and waits for
// their goroutines to exit; it is also invoked when ctx is cancelled.
//
// Each informer gets a watch-error handler that records the failure on state
// (e.g. "forbidden: cannot list resource \"pods\"") and a goroutine that marks
// the kind as synced once its first LIST completes, so the UI can show a
// loading state instead of a misleading empty list.
func StartInformers(ctx context.Context, cs kubernetes.Interface, ns string, state *State) (stop func(), err error) {
	factory := newFactory(cs, ns)

	type reg struct {
		kind Kind
		inf  cache.SharedIndexInformer
		h    cache.ResourceEventHandlerFuncs
	}
	regs := []reg{
		{KindPod, factory.InformerFor(&corev1.Pod{}, newInformerFunc(state, KindPod, &corev1.Pod{},
			func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				return cs.CoreV1().Pods(ns).List(ctx, o)
			},
			func(ctx context.Context, o metav1.ListOptions) (apiwatch.Interface, error) {
				return cs.CoreV1().Pods(ns).Watch(ctx, o)
			})), podHandlers(state)},
		{KindConfigMap, factory.InformerFor(&corev1.ConfigMap{}, newInformerFunc(state, KindConfigMap, &corev1.ConfigMap{},
			func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				return cs.CoreV1().ConfigMaps(ns).List(ctx, o)
			},
			func(ctx context.Context, o metav1.ListOptions) (apiwatch.Interface, error) {
				return cs.CoreV1().ConfigMaps(ns).Watch(ctx, o)
			})), configMapHandlers(state)},
		{KindSecret, factory.InformerFor(&corev1.Secret{}, newInformerFunc(state, KindSecret, &corev1.Secret{},
			func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				return cs.CoreV1().Secrets(ns).List(ctx, o)
			},
			func(ctx context.Context, o metav1.ListOptions) (apiwatch.Interface, error) {
				return cs.CoreV1().Secrets(ns).Watch(ctx, o)
			})), secretHandlers(state)},
		{KindIngress, factory.InformerFor(&networkingv1.Ingress{}, newInformerFunc(state, KindIngress, &networkingv1.Ingress{},
			func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				return cs.NetworkingV1().Ingresses(ns).List(ctx, o)
			},
			func(ctx context.Context, o metav1.ListOptions) (apiwatch.Interface, error) {
				return cs.NetworkingV1().Ingresses(ns).Watch(ctx, o)
			})), ingressHandlers(state)},
	}
	for _, r := range regs {
		kind := r.kind
		if err := r.inf.SetWatchErrorHandlerWithContext(func(_ context.Context, _ *cache.Reflector, err error) {
			state.SetError(kind, err.Error())
		}); err != nil {
			return nil, err
		}
		if kind == KindSecret {
			// The TUI only shows a secret's name, type, and key count. Strip
			// values (and the last-applied annotation, which embeds them) before
			// the object enters the informer cache so secret material is never
			// held in memory cluster-wide. The values still cross the wire on
			// LIST/WATCH: a metadata-only informer would avoid that but cannot
			// supply the TYPE and DATA columns.
			if err := r.inf.SetTransform(stripSecretData); err != nil {
				return nil, err
			}
		}
		if _, err := r.inf.AddEventHandler(r.h); err != nil {
			return nil, err
		}
	}

	stopCh := make(chan struct{})
	var once sync.Once
	stop = func() {
		once.Do(func() {
			close(stopCh)
			factory.Shutdown() // waits for the informer goroutines to exit
		})
	}
	go factory.Start(stopCh)

	for _, r := range regs {
		kind, inf := r.kind, r.inf
		go func() {
			if cache.WaitForCacheSync(stopCh, inf.HasSynced) {
				state.SetSynced(kind)
			}
		}()
	}

	go func() {
		<-ctx.Done()
		stop()
	}()

	return stop, nil
}

// stripSecretData reduces a Secret to the fields the TUI renders. Data keys
// are kept (so the key count is right) with their values dropped.
func stripSecretData(obj interface{}) (interface{}, error) {
	sec, ok := obj.(*corev1.Secret)
	if !ok {
		return obj, nil
	}
	out := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:              sec.Name,
			Namespace:         sec.Namespace,
			UID:               sec.UID,
			ResourceVersion:   sec.ResourceVersion,
			CreationTimestamp: sec.CreationTimestamp,
		},
		Type: sec.Type,
	}
	if len(sec.Data) > 0 {
		out.Data = make(map[string][]byte, len(sec.Data))
		for k := range sec.Data {
			out.Data[k] = nil
		}
	}
	return out, nil
}

// newInformerFunc builds an informer whose list/watch calls report success to
// state. The recovery signal is a successful WATCH establishment: it proves the
// connection works again regardless of whether the resourceVersion moved (it
// doesn't on a quiet cluster), whereas a successful LIST alone proves nothing
// about watch permission. The error generation is read before the call so a
// failure recorded while the call was in flight is never erased by it.
func newInformerFunc(
	state *State, kind Kind, example runtime.Object,
	list func(context.Context, metav1.ListOptions) (runtime.Object, error),
	watch func(context.Context, metav1.ListOptions) (apiwatch.Interface, error),
) func(kubernetes.Interface, time.Duration) cache.SharedIndexInformer {
	return func(client kubernetes.Interface, resync time.Duration) cache.SharedIndexInformer {
		lw := &cache.ListWatch{
			ListWithContextFunc: list,
			WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (apiwatch.Interface, error) {
				gen := state.ErrGen(kind)
				w, err := watch(ctx, o)
				if err == nil {
					state.ClearErrorIfGen(kind, gen)
				}
				return w, err
			},
		}
		return cache.NewSharedIndexInformer(cache.ToListWatcherWithWatchListSemantics(lw, client), example, resync,
			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	}
}

func newFactory(cs kubernetes.Interface, ns string) informers.SharedInformerFactory {
	if ns == "" {
		return informers.NewSharedInformerFactory(cs, 30*time.Second)
	}
	return informers.NewSharedInformerFactoryWithOptions(cs, 30*time.Second,
		informers.WithNamespace(ns))
}

func podHandlers(state *State) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if p, ok := obj.(*corev1.Pod); ok {
				state.UpsertRow(KindPod, toRow(p))
			}
		},
		UpdateFunc: func(_, newObj interface{}) {
			if p, ok := newObj.(*corev1.Pod); ok {
				state.UpsertRow(KindPod, toRow(p))
			}
		},
		DeleteFunc: func(obj interface{}) {
			switch p := obj.(type) {
			case *corev1.Pod:
				state.DeleteRow(KindPod, key(p))
			case cache.DeletedFinalStateUnknown:
				if pod, ok := p.Obj.(*corev1.Pod); ok {
					state.DeleteRow(KindPod, key(pod))
				}
			}
		},
	}
}

func configMapHandlers(state *State) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if cm, ok := obj.(*corev1.ConfigMap); ok {
				state.UpsertRow(KindConfigMap, toConfigMapRow(cm))
			}
		},
		UpdateFunc: func(_, newObj interface{}) {
			if cm, ok := newObj.(*corev1.ConfigMap); ok {
				state.UpsertRow(KindConfigMap, toConfigMapRow(cm))
			}
		},
		DeleteFunc: func(obj interface{}) {
			switch cm := obj.(type) {
			case *corev1.ConfigMap:
				state.DeleteRow(KindConfigMap, cm.Namespace+"/"+cm.Name)
			case cache.DeletedFinalStateUnknown:
				if c, ok := cm.Obj.(*corev1.ConfigMap); ok {
					state.DeleteRow(KindConfigMap, c.Namespace+"/"+c.Name)
				}
			}
		},
	}
}

func secretHandlers(state *State) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if sec, ok := obj.(*corev1.Secret); ok {
				state.UpsertRow(KindSecret, toSecretRow(sec))
			}
		},
		UpdateFunc: func(_, newObj interface{}) {
			if sec, ok := newObj.(*corev1.Secret); ok {
				state.UpsertRow(KindSecret, toSecretRow(sec))
			}
		},
		DeleteFunc: func(obj interface{}) {
			switch sec := obj.(type) {
			case *corev1.Secret:
				state.DeleteRow(KindSecret, sec.Namespace+"/"+sec.Name)
			case cache.DeletedFinalStateUnknown:
				if s, ok := sec.Obj.(*corev1.Secret); ok {
					state.DeleteRow(KindSecret, s.Namespace+"/"+s.Name)
				}
			}
		},
	}
}

func ingressHandlers(state *State) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if ing, ok := obj.(*networkingv1.Ingress); ok {
				state.UpsertRow(KindIngress, toIngressRow(ing))
			}
		},
		UpdateFunc: func(_, newObj interface{}) {
			if ing, ok := newObj.(*networkingv1.Ingress); ok {
				state.UpsertRow(KindIngress, toIngressRow(ing))
			}
		},
		DeleteFunc: func(obj interface{}) {
			switch ing := obj.(type) {
			case *networkingv1.Ingress:
				state.DeleteRow(KindIngress, ing.Namespace+"/"+ing.Name)
			case cache.DeletedFinalStateUnknown:
				if i, ok := ing.Obj.(*networkingv1.Ingress); ok {
					state.DeleteRow(KindIngress, i.Namespace+"/"+i.Name)
				}
			}
		},
	}
}
