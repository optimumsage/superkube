// Package kube wraps the parts of k8s.io/client-go that superkube actually
// uses. We keep this surface small on purpose — client-go is heavy and most
// commands shell out to kubectl. Only features that need structured data
// (events, multi-pod logs, diagnostics for AI prompts) live here.
package kube

import (
	"errors"
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

// Loader resolves the kubeconfig the user expects. Honors --kubeconfig and
// --context overrides from the CLI; everything else (KUBECONFIG env, ~/.kube
// merging) is delegated to client-go's normal precedence.
type Loader struct {
	KubeconfigPath string // --kubeconfig override; empty for default
	Context        string // --context override; empty to use current-context
}

// Raw returns the raw merged kubeconfig (with all contexts visible). Used by
// `sk ctx` / `sk ns` to list and edit.
func (l Loader) Raw() (api.Config, clientcmd.ClientConfig, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if l.KubeconfigPath != "" {
		rules.ExplicitPath = l.KubeconfigPath
	}
	overrides := &clientcmd.ConfigOverrides{}
	if l.Context != "" {
		overrides.CurrentContext = l.Context
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	raw, err := cc.RawConfig()
	if err != nil {
		return api.Config{}, nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return raw, cc, nil
}

// RESTConfig returns the *rest.Config for the resolved context, ready for
// clientset construction.
func (l Loader) RESTConfig() (*rest.Config, error) {
	_, cc, err := l.Raw()
	if err != nil {
		return nil, err
	}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("build REST config: %w", err)
	}
	return cfg, nil
}

// Clientset returns a typed clientset for the resolved context.
func (l Loader) Clientset() (*kubernetes.Clientset, error) {
	cfg, err := l.RESTConfig()
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build clientset: %w", err)
	}
	return cs, nil
}

// CurrentContext returns the name of the active context in the merged config,
// honoring the --context override if set.
func (l Loader) CurrentContext() (string, error) {
	raw, _, err := l.Raw()
	if err != nil {
		return "", err
	}
	if l.Context != "" {
		return l.Context, nil
	}
	if raw.CurrentContext == "" {
		return "", errors.New("no current-context set in kubeconfig")
	}
	return raw.CurrentContext, nil
}

// CurrentNamespace returns the namespace from the resolved context, defaulting
// to "default" when the context doesn't pin a namespace.
func (l Loader) CurrentNamespace() (string, error) {
	raw, _, err := l.Raw()
	if err != nil {
		return "", err
	}
	return namespaceFromRaw(raw, l.Context), nil
}

// CurrentContextAndNamespace returns both the active context name and its
// namespace from a single kubeconfig parse. Callers that need both (the AI
// commands attaching light context) should prefer this over calling
// CurrentContext + CurrentNamespace, which would parse the kubeconfig twice.
func (l Loader) CurrentContextAndNamespace() (contextName, namespace string, err error) {
	raw, _, err := l.Raw()
	if err != nil {
		return "", "", err
	}
	contextName = l.Context
	if contextName == "" {
		contextName = raw.CurrentContext
	}
	if contextName == "" {
		return "", "", errors.New("no current-context set in kubeconfig")
	}
	return contextName, namespaceFromRaw(raw, l.Context), nil
}

// namespaceFromRaw resolves the namespace for contextOverride (or the config's
// current-context when empty), defaulting to "default".
func namespaceFromRaw(raw api.Config, contextOverride string) string {
	name := contextOverride
	if name == "" {
		name = raw.CurrentContext
	}
	ctx, ok := raw.Contexts[name]
	if !ok || ctx == nil || ctx.Namespace == "" {
		return "default"
	}
	return ctx.Namespace
}
