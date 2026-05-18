package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/optimumsage/superkube/internal/ai"
	"github.com/optimumsage/superkube/internal/kubectl"
)

// apiAIExplain streams a free-form AI answer over SSE. Mirrors `sk ai
// explain <question>` — context attachment is opt-in via with_context, and
// the caller may also opt into read-only kubectl/sk tool access via
// allow_read_only (claude only; gemini falls back to prompt-only constraint).
func (s *Server) apiAIExplain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Question      string `json:"question"`
		WithContext   bool   `json:"with_context"`
		AllowReadOnly bool   `json:"allow_read_only"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Question == "" {
		http.Error(w, "question required", http.StatusBadRequest)
		return
	}
	provider, err := s.deps.AIProvider()
	if err != nil || provider == nil {
		http.Error(w, "no AI provider available (install claude or gemini)", http.StatusServiceUnavailable)
		return
	}
	sess := s.readSession(r)
	in := ai.PromptInputs{
		Question:     body.Question,
		Context:      sess.Context,
		Namespace:    sess.Namespace,
		ToolsAllowed: body.AllowReadOnly,
	}
	if !body.WithContext || s.deps.NoContext {
		in.Context = ""
		in.Namespace = ""
	}
	prompt, err := ai.Render("explain", in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.streamAI(w, r, provider, prompt, "explain", ai.RunOpts{AllowReadOnlyKubectl: body.AllowReadOnly})
}

// apiAIDiagnose gathers describe + events + logs + owner chain + siblings
// for the named resource and asks the AI provider for a root-cause
// analysis. Same prompt template as `sk diagnose`.
func (s *Server) apiAIDiagnose(w http.ResponseWriter, r *http.Request) {
	s.runDiagnose(w, r, "diagnose")
}

// apiAIWhy is diagnose with a tighter failure-mode-classification prompt.
func (s *Server) apiAIWhy(w http.ResponseWriter, r *http.Request) {
	s.runDiagnose(w, r, "why")
}

// apiAILogs summarizes a captured tail of logs via the AI provider.
func (s *Server) apiAILogs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Namespace string `json:"namespace"`
		Pod       string `json:"pod"`
		Container string `json:"container"`
		Tail      int64  `json:"tail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Pod == "" {
		http.Error(w, "pod required", http.StatusBadRequest)
		return
	}
	if body.Tail == 0 {
		body.Tail = 200
	}
	provider, err := s.deps.AIProvider()
	if err != nil || provider == nil {
		http.Error(w, "no AI provider", http.StatusServiceUnavailable)
		return
	}
	sess := s.readSession(r)
	args := []string{"logs", body.Pod, "-n", body.Namespace, "--tail", itoa64(body.Tail)}
	if body.Container != "" {
		args = append(args, "-c", body.Container)
	}
	args = sess.prependGlobalFlagsNoNS(args)
	var buf bytes.Buffer
	_ = s.deps.Runner.Run(r.Context(), args, kubectl.RunOpts{Stdout: &buf, Stderr: &buf})

	prompt, err := ai.Render("logs", ai.PromptInputs{
		Resource:  "pod/" + body.Pod,
		Context:   sess.Context,
		Namespace: sess.Namespace,
		Logs:      ai.TruncateLogs(buf.String(), int(body.Tail)),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.streamAI(w, r, provider, prompt, "logs", ai.RunOpts{})
}

// runDiagnose is shared by /ai/diagnose and /ai/why. The only difference is
// the template name and the implicit prompt focus.
func (s *Server) runDiagnose(w http.ResponseWriter, r *http.Request, tmpl string) {
	var body struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "kind/name required", http.StatusBadRequest)
		return
	}
	if body.Kind == "" {
		body.Kind = "pod"
	}
	provider, err := s.deps.AIProvider()
	if err != nil || provider == nil {
		http.Error(w, "no AI provider available", http.StatusServiceUnavailable)
		return
	}

	sess := s.readSession(r)
	if body.Namespace == "" {
		body.Namespace = sess.Namespace
	}
	resource := body.Kind + "/" + body.Name

	// Gather describe/events/logs/owner chain/siblings. Each step is best-
	// effort: a permission error on one shouldn't kill the others.
	describe := s.captureKubectl(r, []string{"describe", body.Kind, body.Name, "-n", body.Namespace})
	events := s.captureKubectl(r, []string{"events", "--for", resource, "-n", body.Namespace})
	logs := ""
	if body.Kind == "pod" {
		logs = s.captureKubectl(r, []string{"logs", body.Name, "-n", body.Namespace, "--tail", "200"})
	}
	ownerChain, sibling := "", ""
	if body.Kind == "pod" {
		l := s.loader(sess)
		ownerChain, _ = l.OwnerChain(r.Context(), body.Namespace, body.Name)
		sibling, _ = l.SiblingPods(r.Context(), body.Namespace, body.Name)
	}

	prompt, err := ai.Render(tmpl, ai.PromptInputs{
		Resource:    resource,
		Context:     sess.Context,
		Namespace:   body.Namespace,
		Describe:    describe,
		Events:      events,
		Logs:        ai.TruncateLogs(logs, 200),
		OwnerChain:  ownerChain,
		SiblingPods: sibling,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.streamAI(w, r, provider, prompt, tmpl, ai.RunOpts{})
}

// streamAI runs the provider with prompt → SSE writer. Each chunk the provider
// emits becomes one "chunk" event. We finish with an "end" event so the
// client can stop the spinner. opts is forwarded to the provider verbatim so
// the caller controls e.g. read-only tool access.
func (s *Server) streamAI(w http.ResponseWriter, r *http.Request, provider ai.Provider, prompt, verb string, opts ai.RunOpts) {
	sse, err := newSSE(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stop := sse.Heartbeat(15 * time.Second)
	defer stop()

	start := time.Now()
	wr := &chunkSSE{sse: sse}
	if err := provider.Run(r.Context(), prompt, wr, opts); err != nil && r.Context().Err() == nil {
		_ = sse.Send("error", map[string]string{"message": err.Error()})
	}
	_ = sse.Send("end", map[string]any{"ms": time.Since(start).Milliseconds()})
	s.recordWebAudit("ai-"+verb, []string{provider.Name()}, 0, time.Since(start))
}

// chunkSSE wraps an SSE writer in an io.Writer that emits a "chunk" event for
// every Write call. AI providers stream tokens/lines as they arrive, so each
// Write maps to one visible chunk for the user.
type chunkSSE struct{ sse *sseWriter }

func (c *chunkSSE) Write(p []byte) (int, error) {
	if err := c.sse.Send("chunk", map[string]string{"text": string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// captureKubectl runs kubectl with args and returns the combined output.
// Errors are swallowed because diagnose handlers want best-effort capture.
func (s *Server) captureKubectl(r *http.Request, args []string) string {
	sess := s.readSession(r)
	full := args
	if hasFlagN(args) {
		full = sess.prependGlobalFlagsNoNS(args)
	} else {
		full = sess.prependGlobalFlags(args)
	}
	var buf bytes.Buffer
	_ = s.deps.Runner.Run(r.Context(), full, kubectl.RunOpts{Stdout: &buf, Stderr: &buf})
	return buf.String()
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	// Tiny stdlib-free int64-to-string. Acceptable for tail counts.
	neg := false
	if v < 0 {
		neg = true
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
