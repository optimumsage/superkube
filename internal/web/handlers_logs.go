package web

import (
	"bufio"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/optimumsage/superkube/internal/kube"
)

// streamLogs tails one container's logs over SSE. Browser uses EventSource;
// each log line becomes a "line" event so the client renders incrementally.
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	pod := r.PathValue("pod")
	container := r.URL.Query().Get("container")
	follow := r.URL.Query().Get("follow") == "1"
	tail, _ := strconv.ParseInt(r.URL.Query().Get("tail"), 10, 64)
	if tail == 0 {
		tail = 200
	}

	sess := s.readSession(r)
	cs, err := s.loader(sess).Clientset()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sse, err := newSSE(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stop := sse.Heartbeat(15 * time.Second)
	defer stop()

	opts := &corev1.PodLogOptions{
		Container: container,
		Follow:    follow,
		TailLines: ptrInt64(tail),
	}
	stream, err := cs.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(r.Context())
	if err != nil {
		_ = sse.Send("error", map[string]string{"message": err.Error()})
		return
	}
	defer stream.Close()

	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if r.Context().Err() != nil {
			break
		}
		_ = sse.Send("line", map[string]any{
			"pod":  pod,
			"line": sc.Text(),
		})
	}
	_ = sse.Send("end", map[string]string{"reason": "eof"})
}

// streamLogsMulti delegates to kube.TailMulti, fanning a workload's pods into
// one SSE stream with per-pod color hints.
func (s *Server) streamLogsMulti(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "target required (deploy/foo, sts/bar, app=web)", http.StatusBadRequest)
		return
	}
	follow := r.URL.Query().Get("follow") == "1"
	tail, _ := strconv.ParseInt(r.URL.Query().Get("tail"), 10, 64)
	if tail == 0 {
		tail = 200
	}

	sess := s.readSession(r)
	sse, err := newSSE(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stop := sse.Heartbeat(15 * time.Second)
	defer stop()

	// TailMulti writes to an io.Writer; we adapt it to "line" SSE events with
	// a small line-buffered writer.
	w2 := &lineSSE{sse: sse}
	loader := s.loader(sess)
	opts := kube.MultiLogOpts{
		Namespace: sess.Namespace,
		Follow:    follow,
		TailLines: tail,
	}
	// Match the CLI: target containing "/" but not "=" is a workload shorthand
	// (deploy/web); anything else is a raw selector (app=web,tier=front).
	if strings.Contains(target, "/") && !strings.Contains(target, "=") {
		opts.Workload = target
	} else {
		opts.Selector = target
	}
	err = loader.TailMulti(r.Context(), opts, w2, defaultPrefixer)
	if err != nil && r.Context().Err() == nil {
		_ = sse.Send("error", map[string]string{"message": err.Error()})
	}
	_ = sse.Send("end", map[string]string{"reason": "context-cancelled"})
}

// lineSSE buffers writes from kube.TailMulti and emits one SSE event per
// complete line.
type lineSSE struct {
	sse *sseWriter
	buf strings.Builder
}

func (l *lineSSE) Write(p []byte) (int, error) {
	l.buf.Write(p)
	s := l.buf.String()
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		line := s[:i]
		// Strip a TailMulti-injected "[pod] " prefix to separate pod tag from
		// the line text. If absent, send the whole line as text.
		var pod string
		if strings.HasPrefix(line, "[") {
			if j := strings.Index(line, "] "); j > 0 {
				pod = line[1:j]
				line = line[j+2:]
			}
		}
		_ = l.sse.Send("line", map[string]any{"pod": pod, "line": line})
		s = s[i+1:]
	}
	l.buf.Reset()
	l.buf.WriteString(s)
	return len(p), nil
}

// defaultPrefixer formats lines as kube.TailMulti expects: "[pod] line".
func defaultPrefixer(podName string, _ int) string {
	return fmt.Sprintf("[%s] ", podName)
}

func ptrInt64(v int64) *int64 { return &v }
