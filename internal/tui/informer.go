package tui

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

// quietClientLogs silences klog (and client-go's contextual logger) while the
// TUI owns the terminal. Reflector/watch failures otherwise print straight to
// stderr and paint over the alt-screen. The returned func restores logging.
// Errors are not lost: StartInformers surfaces them in the status bar via
// State.SetError.
func quietClientLogs() (restore func()) {
	klog.LogToStderr(false)
	klog.SetOutput(io.Discard)
	klog.SetLogger(logr.Discard())
	return func() {
		klog.ClearLogger()
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	}
}

// StartInformers wires informers for Pods, ConfigMaps, Secrets, and Ingresses,
// each writing into state. When ns is empty all namespaces are watched.
// Returns the single stop channel; closing it cancels the underlying factory.
//
// Each informer gets a watch-error handler that records the failure on state
// (e.g. "forbidden: cannot list resource \"pods\"") and a goroutine that marks
// the kind as synced once its first LIST completes, so the UI can show a
// loading state instead of a misleading empty list.
func StartInformers(ctx context.Context, cs kubernetes.Interface, ns string, state *State) (chan struct{}, error) {
	factory := newFactory(cs, ns)

	type reg struct {
		kind Kind
		inf  cache.SharedIndexInformer
		h    cache.ResourceEventHandlerFuncs
	}
	regs := []reg{
		{KindPod, factory.Core().V1().Pods().Informer(), podHandlers(state)},
		{KindConfigMap, factory.Core().V1().ConfigMaps().Informer(), configMapHandlers(state)},
		{KindSecret, factory.Core().V1().Secrets().Informer(), secretHandlers(state)},
		{KindIngress, factory.Networking().V1().Ingresses().Informer(), ingressHandlers(state)},
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

	stop := make(chan struct{})
	go factory.Start(stop)

	for _, r := range regs {
		kind, inf := r.kind, r.inf
		go func() {
			if cache.WaitForCacheSync(stop, inf.HasSynced) {
				state.SetSynced(kind)
			}
		}()
	}

	go func() {
		<-ctx.Done()
		select {
		case <-stop:
		default:
			close(stop)
		}
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
