// Package tui implements `sk tui`: a full-screen Pods browser backed by a
// client-go informer, with row actions that either suspend the TUI and shell
// out to existing superkube subcommands (describe / diagnose / why / events /
// yaml) or stay in-process for a streamed in-TUI log view.
//
// The store is multi-kind (Pods, ConfigMaps, Secrets, Ingresses) so users can
// flip between resource types via the number-key kind switcher without
// rebuilding the model.
package tui

import (
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

// Kind names the resource family a row belongs to. Stored as strings so they
// can be compared, printed, and used as map keys without a separate enum
// helper.
type Kind string

const (
	KindPod       Kind = "pod"
	KindConfigMap Kind = "configmap"
	KindSecret    Kind = "secret"
	KindIngress   Kind = "ingress"
)

// AllKinds is the canonical ordering used by the kind switcher (1=Pods,
// 2=ConfigMaps, 3=Secrets, 4=Ingresses).
var AllKinds = []Kind{KindPod, KindConfigMap, KindSecret, KindIngress}

// Title returns a human label for k ("Pods", "ConfigMaps", ...).
func (k Kind) Title() string {
	switch k {
	case KindPod:
		return "Pods"
	case KindConfigMap:
		return "ConfigMaps"
	case KindSecret:
		return "Secrets"
	case KindIngress:
		return "Ingresses"
	}
	return string(k)
}

// CLIVerb maps a Kind to the `sk` subcommand verb used for edit/delete
// shell-outs. Matches the verbs registered in internal/cli/{configmap,secret,
// ingress}.go.
func (k Kind) CLIVerb() string {
	switch k {
	case KindConfigMap:
		return "configmap"
	case KindSecret:
		return "secret"
	case KindIngress:
		return "ingress"
	}
	return string(k)
}

// Row is the common shape used by the list view and the actions. Each concrete
// row type (PodRow, ConfigMapRow, ...) exposes its namespace/name/age plus a
// kind-specific column slice so renderers can stay generic.
type Row interface {
	GetNamespace() string
	GetName() string
	GetCreated() time.Time
	// Cols returns the values for the kind's table columns, in the order
	// matching ColHeaders(kind). Namespace, Name, and Age are rendered
	// separately and are NOT included here.
	Cols() []string
	// Filterable returns the substrings that the user's filter text is
	// matched against (lowercased by the caller).
	Filterable() []string
}

// PodRow is the flattened, render-ready shape of a Pod. We keep the raw
// CreationTimestamp around for accurate age display on each render tick
// without re-walking the original object. Node / IP / Containers feed the
// details side panel; Status is the kubectl-flavored status string (which
// can differ from Phase, e.g. CrashLoopBackOff).
type PodRow struct {
	Namespace  string
	Name       string
	Phase      string
	Status     string // kubectl-style status (CrashLoopBackOff, Terminating, ...)
	Ready      string // "n/m"
	Restarts   int32
	Created    time.Time
	Node       string
	IP         string
	Containers []string
}

func (r PodRow) GetNamespace() string  { return r.Namespace }
func (r PodRow) GetName() string       { return r.Name }
func (r PodRow) GetCreated() time.Time { return r.Created }
func (r PodRow) Cols() []string {
	return []string{r.Ready, r.Status, itoa(int(r.Restarts))}
}
func (r PodRow) Filterable() []string {
	return []string{r.Namespace, r.Name, r.Status}
}

// ConfigMapRow / SecretRow / IngressRow follow the same shape — namespace,
// name, age, and a small kind-specific summary (data-key count, secret type,
// ingress class). Kept intentionally minimal: the YAML view shows the full
// object; the table is just for navigation.

type ConfigMapRow struct {
	Namespace string
	Name      string
	DataKeys  int
	Created   time.Time
}

func (r ConfigMapRow) GetNamespace() string  { return r.Namespace }
func (r ConfigMapRow) GetName() string       { return r.Name }
func (r ConfigMapRow) GetCreated() time.Time { return r.Created }
func (r ConfigMapRow) Cols() []string        { return []string{itoa(r.DataKeys)} }
func (r ConfigMapRow) Filterable() []string  { return []string{r.Namespace, r.Name} }

type SecretRow struct {
	Namespace string
	Name      string
	Type      string
	DataKeys  int
	Created   time.Time
}

func (r SecretRow) GetNamespace() string  { return r.Namespace }
func (r SecretRow) GetName() string       { return r.Name }
func (r SecretRow) GetCreated() time.Time { return r.Created }
func (r SecretRow) Cols() []string        { return []string{r.Type, itoa(r.DataKeys)} }
func (r SecretRow) Filterable() []string  { return []string{r.Namespace, r.Name, r.Type} }

type IngressRow struct {
	Namespace string
	Name      string
	Class     string
	Hosts     string
	Created   time.Time
}

func (r IngressRow) GetNamespace() string  { return r.Namespace }
func (r IngressRow) GetName() string       { return r.Name }
func (r IngressRow) GetCreated() time.Time { return r.Created }
func (r IngressRow) Cols() []string        { return []string{r.Class, r.Hosts} }
func (r IngressRow) Filterable() []string  { return []string{r.Namespace, r.Name, r.Class, r.Hosts} }

// ColHeaders returns the per-kind table header labels (in addition to the
// always-rendered NAMESPACE / NAME / AGE columns). Order must match Cols().
func ColHeaders(k Kind) []string {
	switch k {
	case KindPod:
		return []string{"READY", "STATUS", "RESTARTS"}
	case KindConfigMap:
		return []string{"DATA"}
	case KindSecret:
		return []string{"TYPE", "DATA"}
	case KindIngress:
		return []string{"CLASS", "HOSTS"}
	}
	return nil
}

// State is the shared store the informers write to and the bubbletea model
// reads from. A single mutex protects the whole snapshot — informer
// throughput is low enough that this is a non-issue, and it keeps the
// boundary trivial.
type State struct {
	mu     sync.Mutex
	byKind map[Kind]map[string]Row
}

func NewState() *State {
	return &State{byKind: map[Kind]map[string]Row{}}
}

// Upsert keeps backwards compatibility for tests/callers that thought of the
// store as pod-only. It delegates to UpsertRow with KindPod.
func (s *State) Upsert(p *corev1.Pod) { s.UpsertRow(KindPod, toRow(p)) }

// Delete keeps backwards compatibility for tests/callers that thought of the
// store as pod-only.
func (s *State) Delete(p *corev1.Pod) {
	s.DeleteRow(KindPod, p.Namespace+"/"+p.Name)
}

// UpsertRow stores row under (kind, ns/name). Concurrent-safe.
func (s *State) UpsertRow(kind Kind, row Row) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byKind[kind] == nil {
		s.byKind[kind] = map[string]Row{}
	}
	s.byKind[kind][rowKey(row)] = row
}

// DeleteRow removes the row at (kind, key); no-op if absent.
func (s *State) DeleteRow(kind Kind, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.byKind[kind]; m != nil {
		delete(m, key)
	}
}

// Snapshot returns the current pods sorted by namespace then name. Kept for
// backwards compatibility with the pod-centric call sites.
func (s *State) Snapshot() []PodRow {
	rows := s.SnapshotKind(KindPod)
	out := make([]PodRow, 0, len(rows))
	for _, r := range rows {
		if p, ok := r.(PodRow); ok {
			out = append(out, p)
		}
	}
	return out
}

// SnapshotKind returns the rows for kind in (namespace, name) order. Returns
// a fresh slice each call so the caller can safely mutate it.
func (s *State) SnapshotKind(kind Kind) []Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byKind[kind]
	out := make([]Row, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetNamespace() != out[j].GetNamespace() {
			return out[i].GetNamespace() < out[j].GetNamespace()
		}
		return out[i].GetName() < out[j].GetName()
	})
	return out
}

func rowKey(r Row) string { return r.GetNamespace() + "/" + r.GetName() }

func key(p *corev1.Pod) string { return p.Namespace + "/" + p.Name }

func toRow(p *corev1.Pod) PodRow {
	ready, total := 0, len(p.Status.ContainerStatuses)
	var restarts int32
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		restarts += cs.RestartCount
	}
	row := PodRow{
		Namespace: p.Namespace,
		Name:      p.Name,
		Phase:     string(p.Status.Phase),
		Status:    derivePodStatus(p),
		Ready:     intRatio(ready, total),
		Restarts:  restarts,
		Created:   p.CreationTimestamp.Time,
		Node:      p.Spec.NodeName,
		IP:        p.Status.PodIP,
	}
	for _, c := range p.Spec.Containers {
		row.Containers = append(row.Containers, c.Name)
	}
	return row
}

func toConfigMapRow(cm *corev1.ConfigMap) ConfigMapRow {
	n := len(cm.Data) + len(cm.BinaryData)
	return ConfigMapRow{
		Namespace: cm.Namespace,
		Name:      cm.Name,
		DataKeys:  n,
		Created:   cm.CreationTimestamp.Time,
	}
}

func toSecretRow(sec *corev1.Secret) SecretRow {
	t := string(sec.Type)
	if t == "" {
		t = "Opaque"
	}
	return SecretRow{
		Namespace: sec.Namespace,
		Name:      sec.Name,
		Type:      t,
		DataKeys:  len(sec.Data),
		Created:   sec.CreationTimestamp.Time,
	}
}

func toIngressRow(ing *networkingv1.Ingress) IngressRow {
	class := ""
	if ing.Spec.IngressClassName != nil {
		class = *ing.Spec.IngressClassName
	}
	var hosts []string
	for _, r := range ing.Spec.Rules {
		if r.Host != "" {
			hosts = append(hosts, r.Host)
		}
	}
	return IngressRow{
		Namespace: ing.Namespace,
		Name:      ing.Name,
		Class:     class,
		Hosts:     strings.Join(hosts, ","),
		Created:   ing.CreationTimestamp.Time,
	}
}

// derivePodStatus mirrors a subset of kubectl's printers/internalversion logic
// so the Status column matches what users see in `kubectl get pods`. We don't
// try to be exhaustive — only the cases that meaningfully differ from Phase
// (Terminating, container waiting/terminated Reason) are handled.
func derivePodStatus(p *corev1.Pod) string {
	if p.DeletionTimestamp != nil {
		return "Terminating"
	}
	// Init container failures bubble up first; if any init container is in a
	// non-Completed waiting/terminated state, that's what kubectl shows.
	for _, cs := range p.Status.InitContainerStatuses {
		switch {
		case cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0:
			if cs.State.Terminated.Reason != "" {
				return "Init:" + cs.State.Terminated.Reason
			}
			return "Init:Error"
		case cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && cs.State.Waiting.Reason != "PodInitializing":
			return "Init:" + cs.State.Waiting.Reason
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
	return string(p.Status.Phase)
}

func intRatio(a, b int) string {
	if b == 0 {
		return "0/0"
	}
	// One-allocation render — avoids fmt for a hot path the informer hits per event.
	s := []byte{}
	s = appendInt(s, a)
	s = append(s, '/')
	s = appendInt(s, b)
	return string(s)
}

func appendInt(buf []byte, n int) []byte {
	if n == 0 {
		return append(buf, '0')
	}
	if n < 0 {
		buf = append(buf, '-')
		n = -n
	}
	var tmp [10]byte
	i := len(tmp)
	for n > 0 {
		i--
		tmp[i] = byte('0' + n%10)
		n /= 10
	}
	return append(buf, tmp[i:]...)
}
