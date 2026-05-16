package kube

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
)

// DeleteContext removes the named context from the merged kubeconfig and
// persists the change. When pruneOrphans is true, any cluster/auth-info entry
// that is no longer referenced by any remaining context is also removed.
//
// If the deleted context was the active current-context, current-context is
// cleared (kubectl behavior). The caller is responsible for handling that.
func (l Loader) DeleteContext(name string, pruneOrphans bool) error {
	raw, _, err := l.Raw()
	if err != nil {
		return err
	}
	ctx, ok := raw.Contexts[name]
	if !ok {
		return fmt.Errorf("context %q not found in kubeconfig", name)
	}
	cluster, authInfo := ctx.Cluster, ctx.AuthInfo
	delete(raw.Contexts, name)
	if raw.CurrentContext == name {
		raw.CurrentContext = ""
	}
	if pruneOrphans {
		// Drop the cluster/auth-info if no other context still references it.
		clusterUsed, authUsed := false, false
		for _, c := range raw.Contexts {
			if c == nil {
				continue
			}
			if c.Cluster == cluster {
				clusterUsed = true
			}
			if c.AuthInfo == authInfo {
				authUsed = true
			}
		}
		if cluster != "" && !clusterUsed {
			delete(raw.Clusters, cluster)
		}
		if authInfo != "" && !authUsed {
			delete(raw.AuthInfos, authInfo)
		}
	}
	return writeKubeconfig(raw, l.KubeconfigPath)
}

// ProbeResult captures the outcome of a reachability check against one
// kubeconfig context. Reachable is true only when the API server answered
// with a version response within the timeout. Err carries the underlying
// failure (DNS, TLS, timeout, 401, …) so the caller can show the user why
// a context was deemed dead.
type ProbeResult struct {
	Context   string
	Reachable bool
	Server    string
	Err       error
}

// ProbeContext does a single round-trip against the API server of name to
// verify that the context is operational. It uses /version (Discovery) so the
// call is cheap and works against any RBAC posture: even an unauthenticated
// caller normally gets a 200 back from /version, while an unreachable cluster
// produces a network-level error. The timeout caps the whole call.
func ProbeContext(ctx context.Context, kubeconfigPath, name string, timeout time.Duration) ProbeResult {
	res := ProbeResult{Context: name}
	loader := Loader{KubeconfigPath: kubeconfigPath, Context: name}
	cfg, err := loader.RESTConfig()
	if err != nil {
		res.Err = err
		return res
	}
	res.Server = cfg.Host
	cfg.Timeout = timeout
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		res.Err = err
		return res
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := cs.Discovery().RESTClient().Get().AbsPath("/version").DoRaw(probeCtx); err != nil {
		res.Err = err
		return res
	}
	res.Reachable = true
	return res
}
