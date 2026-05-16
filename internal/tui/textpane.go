package tui

import (
	"context"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// textProducer is anything that writes bytes to w while running. It is invoked
// in a goroutine — the implementation may block on a subprocess or on a
// streamed AI response. Returning from the function (or honoring ctx) signals
// "done" to the textPane.
type textProducer func(ctx context.Context, w io.Writer) error

// textPane drives the embedded text viewer used for describe / yaml / events /
// diagnose / why. The producer runs in a goroutine and writes into a buffered
// channel; the tea event loop drains it via waitChunk() so we never call
// tea.Program from outside.
type textPane struct {
	title string
	kind  textKind // for the small badge in the header

	body strings.Builder
	// rendered is the body split into lines for filter + viewport math. We
	// rebuild it on every chunk; cheap for kilobyte-scale outputs.
	lines    []string
	filter   string
	filterOn bool

	offset int
	follow bool

	chunkCh chan string
	doneCh  chan error
	cancel  context.CancelFunc
	err     error
	done    bool

	// Bytes received so far — shown in the header so the user can confirm the
	// stream is alive (especially useful for AI which has a startup latency).
	bytes int
}

type textKind int

const (
	textKindDescribe textKind = iota
	textKindYAML
	textKindEvents
	textKindDiagnose
	textKindWhy
)

type textChunkMsg struct{ s string }
type textEndMsg struct{ err error }

func newTextPane(title string, kind textKind) *textPane {
	return &textPane{
		title:   title,
		kind:    kind,
		chunkCh: make(chan string, 64),
		doneCh:  make(chan error, 1),
		follow:  true,
	}
}

// start launches the producer goroutine and returns the tea.Cmd batch that
// waits on the first chunk and the terminal-done message.
func (t *textPane) start(ctx context.Context, prod textProducer) tea.Cmd {
	runCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	go func() {
		defer close(t.chunkCh)
		w := &chanWriter{ch: t.chunkCh, ctx: runCtx}
		err := prod(runCtx, w)
		t.doneCh <- err
	}()
	return tea.Batch(t.waitChunk(), t.waitDone())
}

func (t *textPane) waitChunk() tea.Cmd {
	return func() tea.Msg {
		s, ok := <-t.chunkCh
		if !ok {
			return nil
		}
		return textChunkMsg{s: s}
	}
}

func (t *textPane) waitDone() tea.Cmd {
	return func() tea.Msg {
		err := <-t.doneCh
		return textEndMsg{err: err}
	}
}

func (t *textPane) stop() {
	if t.cancel != nil {
		t.cancel()
	}
}

func (t *textPane) appendChunk(s string) {
	t.body.WriteString(s)
	t.bytes += len(s)
	// Re-split. Cheap; outputs are kilobytes, not megabytes.
	t.lines = strings.Split(t.body.String(), "\n")
}

// filteredLines returns the lines that match the current filter. Empty filter
// returns t.lines as-is (no copy).
func (t *textPane) filteredLines() []string {
	if t.filter == "" {
		return t.lines
	}
	q := strings.ToLower(t.filter)
	out := make([]string, 0, len(t.lines))
	for _, l := range t.lines {
		if strings.Contains(strings.ToLower(l), q) {
			out = append(out, l)
		}
	}
	return out
}

// render paints the body region for the given inner width/height.
func (t *textPane) render(width, height int) string {
	if height < 1 {
		return ""
	}
	lines := t.filteredLines()
	if len(lines) == 0 {
		hint := "(waiting for output…)"
		if t.done {
			hint = "(no output)"
		}
		if t.filter != "" {
			hint = "(no lines match \"" + t.filter + "\")"
		}
		return logSubtle.Render(hint)
	}
	// Follow: snap to bottom.
	if t.follow {
		t.offset = max0(len(lines) - height)
	}
	if t.offset > len(lines)-1 {
		t.offset = len(lines) - 1
	}
	if t.offset < 0 {
		t.offset = 0
	}
	end := t.offset + height
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	for i := t.offset; i < end; i++ {
		line := lines[i]
		if t.filter != "" {
			line = highlight(line, t.filter)
		}
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

func (t *textPane) badge() string {
	switch t.kind {
	case textKindDescribe:
		return badgeStyle.Background(lipgloss.Color("33")).Render(" describe ")
	case textKindYAML:
		return badgeStyle.Background(lipgloss.Color("141")).Render(" yaml ")
	case textKindEvents:
		return badgeStyle.Background(lipgloss.Color("214")).Render(" events ")
	case textKindDiagnose:
		return badgeStyle.Background(lipgloss.Color("39")).Render(" diagnose ")
	case textKindWhy:
		return badgeStyle.Background(lipgloss.Color("13")).Render(" why ")
	}
	return ""
}

func (t *textPane) statusLine() string {
	parts := []string{t.badge(), logEmph.Render(t.title)}
	if !t.done {
		parts = append(parts, logFollowOn.Render("● streaming"))
	} else if t.err != nil {
		parts = append(parts, logErr.Render("✗ "+t.err.Error()))
	} else {
		parts = append(parts, styleHelpKey.Render("✓ done"))
	}
	parts = append(parts, logSubtle.Render(itoa(t.bytes)+" bytes"))
	return strings.Join(parts, "  ")
}

var badgeStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("15"))

// chanWriter adapts an io.Writer to a buffered channel. Honors ctx so a
// cancelled producer doesn't block forever on a full channel.
type chanWriter struct {
	ch  chan<- string
	ctx context.Context
}

func (c *chanWriter) Write(p []byte) (int, error) {
	select {
	case <-c.ctx.Done():
		return 0, c.ctx.Err()
	case c.ch <- string(p):
		return len(p), nil
	}
}
