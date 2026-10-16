package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestHighlightUnicodeSafe(t *testing.T) {
	cases := []struct {
		line, needle string
		wantMatch    bool
	}{
		// strings.ToLower("Ⱥ") is 3 bytes vs 2: byte offsets from the lowered
		// copy used to run past the original and panic.
		{"ȺȺȺ error", "error", true},
		{"İİİ ERROR here", "error", true},
		{"temp 300K ok", "k", true}, // Kelvin sign folds to k
		{"plain", "zzz", false},
		{"ERROR Error error", "error", true},
	}
	for _, c := range cases {
		_ = highlight(c.line, c.needle) // must not panic
		if got := len(foldRegexp(c.needle).FindAllStringIndex(c.line, -1)) > 0; got != c.wantMatch {
			t.Errorf("match(%q,%q) = %v, want %v", c.line, c.needle, got, c.wantMatch)
		}
	}
}

func TestFilteredMatchesUnicodeFold(t *testing.T) {
	v := &logViewer{lines: []string{"ȺȺ Error", "nothing"}, filter: "error"}
	if got := v.filtered(); len(got) != 1 || got[0] != "ȺȺ Error" {
		t.Fatalf("filtered = %v", got)
	}
}

func TestDropLastRune(t *testing.T) {
	if got := dropLastRune("aé世"); got != "aé" {
		t.Errorf("got %q", got)
	}
	if got := dropLastRune(""); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestTextPaneAppendIncremental(t *testing.T) {
	tp := newTextPane("t", textKindYAML)
	for _, c := range []string{"ab", "c\nde", "f\n", "\nlast"} {
		tp.appendChunk(c)
	}
	want := []string{"abc", "def", "", "last"}
	if strings.Join(tp.lines, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q, want %q", tp.lines, want)
	}
}

// A message from a pane that was closed must not land in its replacement.
func TestStalePaneMessagesDropped(t *testing.T) {
	m := newModel(context.Background(), NewState(), Options{})

	old := newTextPane("old", textKindYAML)
	cur := newTextPane("cur", textKindYAML)
	m.text = cur
	m.view = viewText

	next, _ := m.Update(textChunkMsg{id: old.id, s: "stale"})
	m = next.(Model)
	if len(m.text.lines) != 0 {
		t.Fatalf("stale chunk accepted: %q", m.text.lines)
	}
	next, _ = m.Update(textEndMsg{id: old.id, err: errors.New("boom")})
	m = next.(Model)
	if m.text.done || m.text.err != nil {
		t.Fatalf("stale end accepted")
	}
	next, _ = m.Update(textChunkMsg{id: cur.id, s: "fresh"})
	m = next.(Model)
	if len(m.text.lines) != 1 || m.text.lines[0] != "fresh" {
		t.Fatalf("current chunk rejected: %q", m.text.lines)
	}

	lv := newLogViewer("p", "ns", "")
	m.logs = lv
	next, _ = m.Update(logLineMsg{id: lv.id + 100, line: "stale"})
	m = next.(Model)
	if len(m.logs.lines) != 0 {
		t.Fatalf("stale log line accepted")
	}
	// Wrapped cancellation is recognised via errors.Is.
	next, _ = m.Update(logEndMsg{id: lv.id, err: errors.Join(context.Canceled)})
	m = next.(Model)
	if m.logs.errMsg != "" {
		t.Fatalf("wrapped context.Canceled reported as error: %q", m.logs.errMsg)
	}
}

// Run with -race: stop() used to read v.stream while the goroutine wrote it.
func TestLogViewerStopRace(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"}})
	for i := 0; i < 20; i++ {
		v := newLogViewer("p", "ns", "c")
		cmd := v.start(context.Background(), cs)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); v.stop() }()
		_ = cmd
		wg.Wait()
		v.stop()
	}
}

func TestStateSyncAndErrors(t *testing.T) {
	s := NewState()
	if synced, e := s.KindStatus(KindPod); synced || e != "" {
		t.Fatal("fresh state should be unsynced, no error")
	}
	s.SetError(KindPod, "forbidden")
	if _, e := s.KindStatus(KindPod); e != "forbidden" {
		t.Fatal("error not recorded")
	}
	s.UpsertRow(KindPod, PodRow{Namespace: "a", Name: "b"})
	if _, e := s.KindStatus(KindPod); e != "" {
		t.Fatal("upsert should clear error")
	}
	s.SetSynced(KindPod)
	if synced, _ := s.KindStatus(KindPod); !synced {
		t.Fatal("not synced")
	}
}

func TestStartInformersSurfacesWatchErrorsAndSyncs(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("pods is forbidden: cannot list resource \"pods\"")
	})
	state := NewState()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := StartInformers(ctx, cs, "", state); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var podErr string
	var cmSynced bool
	for time.Now().Before(deadline) {
		_, podErr = state.KindStatus(KindPod)
		cmSynced, _ = state.KindStatus(KindConfigMap)
		if podErr != "" && cmSynced {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(podErr, "forbidden") {
		t.Errorf("pod watch error not surfaced: %q", podErr)
	}
	if !cmSynced {
		t.Errorf("configmaps never synced")
	}
	if synced, _ := state.KindStatus(KindPod); synced {
		t.Errorf("pods should not be marked synced after list failure")
	}
}

func TestStripSecretData(t *testing.T) {
	in := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "s", Namespace: "n", ResourceVersion: "7",
			Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "SECRET"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"a": []byte("hunter2"), "b": []byte("x")},
	}
	out, err := stripSecretData(in)
	if err != nil {
		t.Fatal(err)
	}
	sec := out.(*corev1.Secret)
	if len(sec.Data) != 2 || sec.Data["a"] != nil || len(sec.Annotations) != 0 {
		t.Fatalf("secret not stripped: %+v", sec)
	}
	if toSecretRow(sec).DataKeys != 2 {
		t.Fatal("key count must survive stripping")
	}
	if sec.ResourceVersion != "7" || sec.Name != "s" {
		t.Fatal("identity fields lost")
	}
}
