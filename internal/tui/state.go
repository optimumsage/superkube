// Package tui implements `sk tui`: a full-screen Pods browser backed by a
// client-go informer, with row actions that suspend the TUI and shell out to
// existing superkube subcommands (describe / logs / diagnose).
package tui

import (
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// PodRow is the flattened, render-ready shape of a Pod. We keep the raw
// CreationTimestamp around for accurate age display on each render tick
// without re-walking the original object.
type PodRow struct {
	Namespace string
	Name      string
	Phase     string
	Ready     string // "n/m"
	Restarts  int32
	Created   time.Time
}

// State is the shared store the informer writes to and the bubbletea model
// reads from. A single mutex protects the whole snapshot — pod-event throughput
// is low enough that this is a non-issue, and it keeps the boundary trivial.
type State struct {
	mu   sync.Mutex
	pods map[string]PodRow // key: ns/name
}

func NewState() *State { return &State{pods: map[string]PodRow{}} }

func (s *State) Upsert(p *corev1.Pod) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pods[key(p)] = toRow(p)
}

func (s *State) Delete(p *corev1.Pod) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pods, key(p))
}

// Snapshot returns the current pods sorted by namespace then name. Stable
// ordering keeps the cursor pointing at "the same" row across redraws.
func (s *State) Snapshot() []PodRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PodRow, 0, len(s.pods))
	for _, r := range s.pods {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

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
	return PodRow{
		Namespace: p.Namespace,
		Name:      p.Name,
		Phase:     string(p.Status.Phase),
		Ready:     intRatio(ready, total),
		Restarts:  restarts,
		Created:   p.CreationTimestamp.Time,
	}
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
