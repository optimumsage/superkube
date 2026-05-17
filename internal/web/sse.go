package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// sseWriter is a tiny helper that adapts an http.ResponseWriter into the
// Server-Sent Events wire format. It handles framing (event: + data: + blank
// line), periodic heartbeat (so intermediaries don't time-out idle streams),
// and serialized writes (multiple goroutines may push events on the same
// stream, e.g. heartbeat + payload).
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	mu      sync.Mutex
}

// newSSE upgrades the response to text/event-stream and returns a writer.
// Returns an error if the underlying ResponseWriter does not support
// flushing — without flushing, SSE just doesn't work.
func newSSE(w http.ResponseWriter) (*sseWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("response writer does not support flushing")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // defeat nginx/proxy buffering if any
	// Don't write a status here — the first event call below will flush an
	// implicit 200.
	return &sseWriter{w: w, flusher: flusher}, nil
}

// Send marshals data as JSON and emits a named event. Safe for concurrent use.
func (s *sseWriter) Send(event string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if event != "" {
		if _, err := fmt.Fprintf(s.w, "event: %s\n", event); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", payload); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// Comment writes an SSE comment line. We use these for heartbeat: comments are
// ignored by the browser's EventSource but keep the TCP connection warm.
func (s *sseWriter) Comment(msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", msg); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// Heartbeat starts a goroutine that sends a comment every interval until done
// is closed. Returns a cancel function that stops the goroutine. Callers
// should defer the cancel function to ensure the goroutine exits.
func (s *sseWriter) Heartbeat(interval time.Duration) (cancel func()) {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := s.Comment("hb"); err != nil {
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// Write satisfies io.Writer. Each call sends one "data" event containing the
// raw bytes. Useful for piping io.Writer-driven streams (like ai.Provider.Run
// or kube.TailMulti) straight into SSE without an intermediate buffer.
func (s *sseWriter) Write(p []byte) (int, error) {
	if err := s.Send("data", string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}
