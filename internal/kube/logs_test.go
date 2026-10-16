package kube

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func podWith(name string, containers ...string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID("uid-" + name), Labels: map[string]string{"app": "web"}}}
	for _, c := range containers {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: c})
	}
	p.Status.Phase = corev1.PodRunning
	return p
}

func TestContainersToTail(t *testing.T) {
	p := podWith("p", "app", "sidecar")
	if got := containersToTail(p, MultiLogOpts{}); len(got) != 1 || got[0] != "app" {
		t.Errorf("first container expected, got %v", got)
	}
	p.Annotations = map[string]string{defaultContainerAnnotation: "sidecar"}
	if got := containersToTail(p, MultiLogOpts{}); got[0] != "sidecar" {
		t.Errorf("annotation ignored: %v", got)
	}
	p.Annotations[defaultContainerAnnotation] = "bogus"
	if got := containersToTail(p, MultiLogOpts{}); got[0] != "app" {
		t.Errorf("bogus annotation should fall back: %v", got)
	}
	if got := containersToTail(p, MultiLogOpts{Container: "x"}); got[0] != "x" {
		t.Errorf("explicit container ignored: %v", got)
	}
	p.Spec.InitContainers = []corev1.Container{{Name: "init"}}
	if got := containersToTail(p, MultiLogOpts{AllContainers: true}); len(got) != 3 {
		t.Errorf("all containers: %v", got)
	}
}

func TestEmitLinesLongLine(t *testing.T) {
	long := strings.Repeat("x", 3<<20)
	var got []string
	err := emitLines(strings.NewReader("a\r\n"+long+"\ntail"), func(_ int, l string) { got = append(got, l) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "a" || len(got[1]) != len(long) || got[2] != "tail" {
		t.Fatalf("unexpected lines (n=%d)", len(got))
	}
}

func logContainers(cs *fake.Clientset) []string {
	var out []string
	for _, a := range cs.Actions() {
		if ga, ok := a.(k8stesting.GenericAction); ok && a.GetSubresource() == "log" {
			if o, ok := ga.GetValue().(*corev1.PodLogOptions); ok {
				out = append(out, o.Container)
			}
		}
	}
	return out
}

func TestTailMultiPicksContainer(t *testing.T) {
	cs := fake.NewSimpleClientset(podWith("a", "main", "side"))
	var buf bytes.Buffer
	if err := tailMulti(context.Background(), cs, MultiLogOpts{Namespace: "ns", Selector: "app=web"}, &buf, nil); err != nil {
		t.Fatal(err)
	}
	got := logContainers(cs)
	if len(got) != 1 || got[0] != "main" {
		t.Fatalf("containers requested = %v, want [main]", got)
	}
	if !strings.Contains(buf.String(), "[a] ") {
		t.Errorf("output: %q", buf.String())
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func TestTailMultiFollowPicksUpNewPods(t *testing.T) {
	cs := fake.NewSimpleClientset(podWith("a", "main"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &lockedBuf{}
	done := make(chan error, 1)
	go func() {
		done <- tailMulti(ctx, cs, MultiLogOpts{Namespace: "ns", Selector: "app=web", Follow: true}, out, nil)
	}()
	time.Sleep(300 * time.Millisecond)
	if _, err := cs.CoreV1().Pods("ns").Create(ctx, podWith("b", "main"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "[b] ") {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if !strings.Contains(out.String(), "[b] ") {
		t.Fatalf("new pod b never streamed; output: %q", out.String())
	}
}
