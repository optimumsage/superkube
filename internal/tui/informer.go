package tui

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// StartPodInformer wires a shared informer for Pods that writes every change
// into state. When ns is empty the informer watches all namespaces. Returns
// the stop channel so the caller can shut down the factory cleanly on quit.
func StartPodInformer(ctx context.Context, cs kubernetes.Interface, ns string, state *State) (chan struct{}, error) {
	var factory informers.SharedInformerFactory
	if ns == "" {
		factory = informers.NewSharedInformerFactory(cs, 30*time.Second)
	} else {
		factory = informers.NewSharedInformerFactoryWithOptions(cs, 30*time.Second,
			informers.WithNamespace(ns))
	}

	podInformer := factory.Core().V1().Pods().Informer()
	_, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if p, ok := obj.(*corev1.Pod); ok {
				state.Upsert(p)
			}
		},
		UpdateFunc: func(_, newObj interface{}) {
			if p, ok := newObj.(*corev1.Pod); ok {
				state.Upsert(p)
			}
		},
		DeleteFunc: func(obj interface{}) {
			switch p := obj.(type) {
			case *corev1.Pod:
				state.Delete(p)
			case cache.DeletedFinalStateUnknown:
				if pod, ok := p.Obj.(*corev1.Pod); ok {
					state.Delete(pod)
				}
			}
		},
	})
	if err != nil {
		return nil, err
	}

	stop := make(chan struct{})
	go factory.Start(stop)

	// Stop the factory when the parent context cancels.
	go func() {
		<-ctx.Done()
		select {
		case <-stop:
			// already closed
		default:
			close(stop)
		}
	}()

	return stop, nil
}
