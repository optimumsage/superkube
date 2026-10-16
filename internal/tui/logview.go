package tui

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Log buffer cap. Old lines drop off the front of the ring when we exceed
// this. 5000 keeps a meaningful tail without blowing memory.
const logBufferCap = 5000

// logViewer holds the in-TUI log streamer state. The viewport scrolling and
// filtering all happen inside the bubbletea event loop; the streamer lives in
// a goroutine and pushes lines through a channel that the model drains via
// tea.Cmd, so we never call tea.Program from outside.
type logViewer struct {
	// id tags every message this viewer emits so the model can drop stragglers
	// from a pane the user already closed (see paneSeq).
	id        uint64
	pod       string
	namespace string
	container string

	lines  []string // ring buffer
	filter string

	offset int // top of viewport, in *filtered* index space
	follow bool

	cancel   context.CancelFunc
	streamCh chan string
	doneCh   chan error
	closed   bool
	closeMu  sync.Mutex

	errMsg string
}

// logLineMsg arrives when the streamer emits a line.
type logLineMsg struct {
	id   uint64
	line string
}

// logEndMsg arrives when the stream closes (pod ended, error, or cancel).
type logEndMsg struct {
	id  uint64
	err error
}

// paneSeq hands out unique IDs to log/text panes. Messages from a pane's
// goroutine carry its ID; a late message from a cancelled pane must not land
// in the pane the user opened next.
var paneSeq atomic.Uint64

func newLogViewer(pod, ns, container string) *logViewer {
	return &logViewer{
		id:        paneSeq.Add(1),
		pod:       pod,
		namespace: ns,
		container: container,
		follow:    true,
		streamCh:  make(chan string, 256),
		doneCh:    make(chan error, 1),
	}
}

// start kicks off the log stream goroutine and returns the first tea.Cmd that
// waits for a line. Each received logLineMsg should re-arm via waitLine() to
// keep the pipeline running.
func (v *logViewer) start(ctx context.Context, cs kubernetes.Interface) tea.Cmd {
	streamCtx, cancel := context.WithCancel(ctx)
	v.cancel = cancel

	go func() {
		defer close(v.streamCh)
		var tail int64 = 500
		req := cs.CoreV1().Pods(v.namespace).GetLogs(v.pod, &corev1.PodLogOptions{
			Follow:    true,
			Container: v.container,
			TailLines: &tail,
		})
		stream, err := req.Stream(streamCtx)
		if err != nil {
			v.doneCh <- err
			return
		}
		// No v.stream field: cancelling streamCtx closes the body, so stop()
		// needs no handle on it (and sharing one across goroutines raced).
		defer stream.Close()
		scanner := bufio.NewScanner(stream)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			select {
			case <-streamCtx.Done():
				v.doneCh <- streamCtx.Err()
				return
			case v.streamCh <- scanner.Text():
			}
		}
		v.doneCh <- scanner.Err()
	}()

	return tea.Batch(v.waitLine(), v.waitDone())
}

// waitLine returns a tea.Cmd that blocks on the next streamed line. Returns
// nil on closed channel — the doneCh path emits the terminal logEndMsg.
func (v *logViewer) waitLine() tea.Cmd {
	return func() tea.Msg {
		s, ok := <-v.streamCh
		if !ok {
			return nil
		}
		return logLineMsg{id: v.id, line: s}
	}
}

func (v *logViewer) waitDone() tea.Cmd {
	return func() tea.Msg {
		err := <-v.doneCh
		return logEndMsg{id: v.id, err: err}
	}
}

func (v *logViewer) append(line string) {
	v.lines = append(v.lines, line)
	if len(v.lines) > logBufferCap {
		v.lines = v.lines[len(v.lines)-logBufferCap:]
	}
}

func (v *logViewer) stop() {
	v.closeMu.Lock()
	defer v.closeMu.Unlock()
	if v.closed {
		return
	}
	v.closed = true
	if v.cancel != nil {
		v.cancel()
	}
}

// filtered returns the indices into v.lines that match the current filter.
// Empty filter returns the slice itself (cheap, no copy).
func (v *logViewer) filtered() []string {
	if v.filter == "" {
		return v.lines
	}
	re := foldRegexp(v.filter)
	out := make([]string, 0, len(v.lines))
	for _, l := range v.lines {
		if re.MatchString(l) {
			out = append(out, l)
		}
	}
	return out
}

// render produces the log viewport content for the given inner width/height.
// The header row, filter row, and help row are rendered by the model; this
// method only paints the log body.
func (v *logViewer) render(width, height int) string {
	if height < 1 {
		return ""
	}
	lines := v.filtered()
	if len(lines) == 0 {
		hint := "(no log lines yet)"
		if v.filter != "" {
			hint = "(no lines match \"" + v.filter + "\")"
		}
		return logSubtle.Render(hint)
	}

	// Clamp offset so the viewport always shows something.
	if v.follow {
		v.offset = max0(len(lines) - height)
	}
	if v.offset > len(lines)-1 {
		v.offset = len(lines) - 1
	}
	if v.offset < 0 {
		v.offset = 0
	}
	end := v.offset + height
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	for i := v.offset; i < end; i++ {
		line := lines[i]
		if v.filter != "" {
			line = highlight(line, v.filter)
		}
		// Truncate visually — long log lines wrap badly in a bounded box.
		if w := lipgloss.Width(line); w > width {
			line = lipgloss.NewStyle().MaxWidth(width).Render(line)
		}
		b.WriteString(line)
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// foldCache memoizes compiled case-insensitive matchers; render calls the
// highlighter once per visible line per frame, so recompiling would be wasteful.
var (
	foldMu    sync.Mutex
	foldCache = map[string]*regexp.Regexp{}
)

// foldRegexp returns a case-insensitive literal matcher for needle. We use
// regexp (?i) with QuoteMeta rather than lower-casing the haystack because
// strings.ToLower can change byte length ("İ" -> "i̇", "Ⱥ" -> "ⱥ"), so byte
// offsets found in the lowered copy are not valid in the original string.
// Offsets reported by regexp always refer to the string that was matched.
func foldRegexp(needle string) *regexp.Regexp {
	foldMu.Lock()
	defer foldMu.Unlock()
	if re, ok := foldCache[needle]; ok {
		return re
	}
	if len(foldCache) > 64 {
		foldCache = map[string]*regexp.Regexp{}
	}
	re := regexp.MustCompile("(?i)" + regexp.QuoteMeta(needle))
	foldCache[needle] = re
	return re
}

// highlight wraps every case-insensitive occurrence of needle in line with a
// styled span. Matching happens on the original string, so it is safe for any
// Unicode input.
func highlight(line, needle string) string {
	if needle == "" || line == "" {
		return line
	}
	locs := foldRegexp(needle).FindAllStringIndex(line, -1)
	if len(locs) == 0 {
		return line
	}
	var b strings.Builder
	i := 0
	for _, loc := range locs {
		b.WriteString(line[i:loc[0]])
		b.WriteString(logMatchStyle.Render(line[loc[0]:loc[1]]))
		i = loc[1]
	}
	b.WriteString(line[i:])
	return b.String()
}

// statusLine renders the small header line shown above the log viewport.
func (v *logViewer) statusLine() string {
	parts := []string{
		"pod " + logEmph.Render(v.namespace+"/"+v.pod),
	}
	if v.container != "" {
		parts = append(parts, "container "+logEmph.Render(v.container))
	}
	if v.follow {
		parts = append(parts, logFollowOn.Render("● follow"))
	} else {
		parts = append(parts, logFollowOff.Render("○ paused"))
	}
	parts = append(parts, fmt.Sprintf("%d lines", len(v.lines)))
	if v.errMsg != "" {
		parts = append(parts, logErr.Render("error: "+v.errMsg))
	}
	return strings.Join(parts, "  ·  ")
}

var (
	logSubtle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	logEmph       = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	logFollowOn   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	logFollowOff  = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	logErr        = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	logMatchStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("11"))
)
