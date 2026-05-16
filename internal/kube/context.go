package kube

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

// ListContexts returns the sorted list of context names from the merged
// kubeconfig.
func (l Loader) ListContexts() ([]string, error) {
	raw, _, err := l.Raw()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(raw.Contexts))
	for n := range raw.Contexts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// SwitchContext sets current-context to name, persists, and stamps the
// previous-context state file so `sk ctx -` can go back. Returns an error if
// the name doesn't exist in the merged config.
func (l Loader) SwitchContext(name string, stateDir string) error {
	raw, _, err := l.Raw()
	if err != nil {
		return err
	}
	if _, ok := raw.Contexts[name]; !ok {
		return fmt.Errorf("context %q not found in kubeconfig", name)
	}
	if raw.CurrentContext == name {
		// No-op, but still update the previous-context pointer? No: we want
		// `sk ctx -` to flip back to the last *different* context.
		return nil
	}
	prev := raw.CurrentContext
	raw.CurrentContext = name
	if err := writeKubeconfig(raw, l.KubeconfigPath); err != nil {
		return err
	}
	if prev != "" {
		_ = writeState(filepath.Join(stateDir, "previous-context"), prev)
	}
	return nil
}

// PreviousContext returns the most recent prior current-context, or "" if no
// state file has been written yet.
func PreviousContext(stateDir string) string {
	b, err := os.ReadFile(filepath.Join(stateDir, "previous-context"))
	if err != nil {
		return ""
	}
	return string(b)
}

// ListNamespaces does NOT call out to the cluster; it returns the namespace
// pinned on each context plus the special wildcard "" entry. For the active
// list of cluster namespaces (which is what users usually want for `sk ns`),
// callers should use the clientset's CoreV1().Namespaces().List. Kept as a
// separate helper because that requires a network round-trip.
func (l Loader) ListNamespaces() ([]string, error) {
	raw, _, err := l.Raw()
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, ctx := range raw.Contexts {
		if ctx.Namespace != "" {
			seen[ctx.Namespace] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// SwitchNamespace updates the namespace on the active context, persists, and
// stamps the previous-namespace state file.
func (l Loader) SwitchNamespace(namespace string, stateDir string) error {
	raw, _, err := l.Raw()
	if err != nil {
		return err
	}
	current := raw.CurrentContext
	if l.Context != "" {
		current = l.Context
	}
	ctx, ok := raw.Contexts[current]
	if !ok || ctx == nil {
		return fmt.Errorf("current context %q not found in kubeconfig", current)
	}
	if ctx.Namespace == namespace {
		return nil
	}
	prev := ctx.Namespace
	ctx.Namespace = namespace
	if err := writeKubeconfig(raw, l.KubeconfigPath); err != nil {
		return err
	}
	if prev != "" {
		_ = writeState(filepath.Join(stateDir, "previous-namespace"), prev)
	}
	return nil
}

// PreviousNamespace returns the most recent prior namespace for the current
// context. Stored separately from previous-context.
func PreviousNamespace(stateDir string) string {
	b, err := os.ReadFile(filepath.Join(stateDir, "previous-namespace"))
	if err != nil {
		return ""
	}
	return string(b)
}

// writeKubeconfig persists raw back to disk, using the explicit kubeconfig path
// if provided, otherwise the user's default.
func writeKubeconfig(raw api.Config, explicitPath string) error {
	if explicitPath != "" {
		return clientcmd.WriteToFile(raw, explicitPath)
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	// ModifyConfig handles merging across the rules' precedence chain.
	return clientcmd.ModifyConfig(rules, raw, true)
}

func writeState(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(contents), 0o600)
}
