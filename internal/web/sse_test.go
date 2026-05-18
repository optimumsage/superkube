package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSEWriterFramesEvents(t *testing.T) {
	rr := httptest.NewRecorder()
	sse, err := newSSE(rr)
	if err != nil {
		t.Fatalf("newSSE: %v", err)
	}
	if err := sse.Send("hello", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := sse.Comment("hb"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "event: hello\n") {
		t.Errorf("missing event header in %q", body)
	}
	if !strings.Contains(body, `data: {"k":"v"}`) {
		t.Errorf("missing data payload in %q", body)
	}
	if !strings.Contains(body, ": hb\n") {
		t.Errorf("missing heartbeat comment in %q", body)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
}

func TestSSEWriterRequiresFlusher(t *testing.T) {
	// httptest.ResponseRecorder *does* implement Flusher, so build a wrapper
	// that doesn't.
	w := &noFlush{header: http.Header{}}
	if _, err := newSSE(w); err == nil {
		t.Errorf("expected error for non-flusher writer")
	}
}

type noFlush struct {
	header http.Header
	code   int
	body   []byte
}

func (n *noFlush) Header() http.Header         { return n.header }
func (n *noFlush) Write(p []byte) (int, error) { n.body = append(n.body, p...); return len(p), nil }
func (n *noFlush) WriteHeader(code int)        { n.code = code }
