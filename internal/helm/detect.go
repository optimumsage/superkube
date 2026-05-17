package helm

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/optimumsage/superkube/internal/kube"
)

// helmReleaseSecretType is Helm 3's storage driver value: every release
// revision is one Secret of this type in the release's namespace.
const helmReleaseSecretType = "helm.sh/release.v1"

// helmReleaseNamePrefix is the prefix Helm uses on each release-revision
// Secret: "sh.helm.release.v1.<release>.v<revision>".
const helmReleaseNamePrefix = "sh.helm.release.v1."

// DetectReleases lists Helm 3 release secrets across all namespaces the caller
// can see, deduplicating by (namespace, name) and keeping the highest revision.
// Works without the helm binary installed — this is the signal that lets the
// UI show "Helm releases detected" even when `helm` is missing from PATH.
//
// `namespace` scopes the lookup: "" means all namespaces (preferred), otherwise
// only that single namespace. Returns ([], nil) on a cluster query error so
// the caller can degrade gracefully (e.g. dashboard load).
func DetectReleases(ctx context.Context, loader kube.Loader, namespace string) ([]ReleaseRef, error) {
	cs, err := loader.Clientset()
	if err != nil {
		return nil, fmt.Errorf("clientset: %w", err)
	}
	return detectWithClientset(ctx, cs, namespace)
}

func detectWithClientset(ctx context.Context, cs kubernetes.Interface, namespace string) ([]ReleaseRef, error) {
	// Field selector narrows the list to Helm release secrets at the apiserver,
	// keeping the response small even on a busy cluster.
	opts := metav1.ListOptions{
		FieldSelector: "type=" + helmReleaseSecretType,
	}
	secrets, err := cs.CoreV1().Secrets(namespace).List(ctx, opts)
	if err != nil {
		// Fall back to a label-selector list — some api-server versions reject
		// `type=` as a field selector across all namespaces.
		opts.FieldSelector = ""
		opts.LabelSelector = "owner=helm"
		secrets, err = cs.CoreV1().Secrets(namespace).List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("list helm secrets: %w", err)
		}
	}

	// One secret per revision; keep the highest revision per (ns, name).
	type key struct{ ns, name string }
	best := map[key]int{}
	for _, sec := range secrets.Items {
		if sec.Type != "" && sec.Type != helmReleaseSecretType {
			continue
		}
		name, rev := parseReleaseSecretName(sec.Name)
		if name == "" {
			name = sec.Labels["name"]
		}
		if rev == 0 {
			if v, err := strconv.Atoi(sec.Labels["version"]); err == nil {
				rev = v
			}
		}
		if name == "" {
			continue
		}
		k := key{ns: sec.Namespace, name: name}
		if rev > best[k] {
			best[k] = rev
		}
	}

	out := make([]ReleaseRef, 0, len(best))
	for k, rev := range best {
		out = append(out, ReleaseRef{Name: k.name, Namespace: k.ns, Revision: rev})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// parseReleaseSecretName extracts the release name and revision number from a
// Helm 3 release-secret name like "sh.helm.release.v1.<name>.v<revision>".
// Returns ("", 0) if the name does not match the expected shape.
func parseReleaseSecretName(s string) (name string, revision int) {
	if !strings.HasPrefix(s, helmReleaseNamePrefix) {
		return "", 0
	}
	rest := strings.TrimPrefix(s, helmReleaseNamePrefix)
	// Find the LAST ".v<digits>" suffix — release names themselves may contain
	// dots, but the revision is always last.
	dot := strings.LastIndex(rest, ".v")
	if dot <= 0 || dot == len(rest)-2 {
		return "", 0
	}
	name = rest[:dot]
	revStr := rest[dot+2:]
	rev, err := strconv.Atoi(revStr)
	if err != nil {
		return "", 0
	}
	return name, rev
}
