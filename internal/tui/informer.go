package tui

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// StartInformers wires informers for Pods, ConfigMaps, Secrets, and Ingresses,
// each writing into state. When ns is empty all namespaces are watched.
// Returns the single stop channel; closing it cancels the underlying factory.
func StartInformers(ctx context.Context, cs kubernetes.Interface, ns string, state *State) (chan struct{}, error) {
	factory := newFactory(cs, ns)

	if _, err := factory.Core().V1().Pods().Informer().AddEventHandler(podHandlers(state)); err != nil {
		return nil, err
	}
	if _, err := factory.Core().V1().ConfigMaps().Informer().AddEventHandler(configMapHandlers(state)); err != nil {
		return nil, err
	}
	if _, err := factory.Core().V1().Secrets().Informer().AddEventHandler(secretHandlers(state)); err != nil {
		return nil, err
	}
	if _, err := factory.Networking().V1().Ingresses().Informer().AddEventHandler(ingressHandlers(state)); err != nil {
		return nil, err
	}

	stop := make(chan struct{})
	go factory.Start(stop)

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

// StartPodInformer remains for callers that only need pod-watching. The
// multi-kind StartInformers above is the modern path; this thin wrapper keeps
// older entrypoints and tests compiling.
func StartPodInformer(ctx context.Context, cs kubernetes.Interface, ns string, state *State) (chan struct{}, error) {
	factory := newFactory(cs, ns)
	if _, err := factory.Core().V1().Pods().Informer().AddEventHandler(podHandlers(state)); err != nil {
		return nil, err
	}
	stop := make(chan struct{})
	go factory.Start(stop)
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
