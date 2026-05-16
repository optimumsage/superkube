// Package picker implements a centered, full-screen fuzzy picker built on
// bubbletea. It replaces huh's Select for `sk ctx` and `sk ns` because huh's
// internal viewport had two papercuts on Warp:
//
//   - Filtering didn't reset the viewport offset, so newly-filtered matches
//     sometimes started below the visible region (the "press up arrow to see
//     them" bug).
//   - huh doesn't render into the alt screen, so on terminals with a sticky
//     top chrome (Warp) the title row could be eaten by the chrome.
//
// This picker uses tea.WithAltScreen + lipgloss.Place to vertically center the
// box, and always resets the offset on every filter mutation.
package picker

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// marqueeInterval is the redraw cadence for the horizontal scroll on the
// selected row. Slow enough that the eye can track each shift, fast enough that
// long ARN-style context names don't feel sluggish.
const marqueeInterval = 220 * time.Millisecond

// marqueeSeparator is the visible gap stitched between the end and start of a
// scrolling label so the user can tell when a wrap has happened.
const marqueeSeparator = "   ·   "

// marqueeTickMsg is the periodic redraw signal that advances the marquee
// offset. We use a struct (rather than time.Time) so it's trivially identified
// in the Update switch.
type marqueeTickMsg struct{}

// Item is one selectable row. Label is what the user sees; Value is what gets
// returned. Hint is rendered subtly to the right of the label (e.g. "current").
type Item struct {
	Label string
	Value string
	Hint  string
}

// Config configures the picker. Title shows above the filter input. Current is
// the value pre-selected when the picker opens (cursor lands on it).
type Config struct {
	Title   string
	Items   []Item
	Current string
	// Placeholder is shown faintly inside the filter when empty.
	Placeholder string
}

// Run shows the picker and blocks until the user picks (returns value, true)
// or cancels with esc/ctrl+c (returns "", false). Any other error from the
// bubbletea program is surfaced.
func Run(cfg Config) (string, bool, error) {
	m := newModel(cfg)
	p := tea.NewProgram(m, tea.WithAltScreen())
	out, err := p.Run()
	if err != nil {
		return "", false, err
	}
	mm := out.(model)
	if mm.cancelled || !mm.picked {
		return "", false, nil
	}
	return mm.result, true, nil
}

type model struct {
	cfg      Config
	input    textinput.Model
	filtered []Item

	cursor int // index into filtered
	top    int // viewport top into filtered
	w, h   int

	// marqueeOff is the rune-offset into the selected row's scrolling text.
	// Reset to 0 whenever the cursor moves or the filter changes so the user
	// always sees the start of the newly-focused label first.
	marqueeOff int

	picked    bool
	cancelled bool
	result    string
}

func newModel(cfg Config) model {
	ti := textinput.New()
	ti.Placeholder = cfg.Placeholder
	if ti.Placeholder == "" {
		ti.Placeholder = "type to filter…"
	}
	ti.Prompt = "› "
	ti.Focus()
	ti.CharLimit = 256

	m := model{cfg: cfg, input: ti}
	m.applyFilter()
	// Land the cursor on the current selection if it survives the (empty) filter.
	for i, it := range m.filtered {
		if it.Value == cfg.Current {
			m.cursor = i
			break
		}
	}
	return m
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, marqueeTickCmd())
}

// marqueeTickCmd schedules the next marquee tick. We reschedule from each
// tickMsg handler so the cadence stays consistent even if the program is
// briefly suspended (window resize, etc.).
func marqueeTickCmd() tea.Cmd {
	return tea.Tick(marqueeInterval, func(time.Time) tea.Msg { return marqueeTickMsg{} })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.clampCursor()
		return m, nil

	case marqueeTickMsg:
		m.marqueeOff++
		return m, marqueeTickCmd()

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "enter":
			if it, ok := m.current(); ok {
				m.picked = true
				m.result = it.Value
				return m, tea.Quit
			}
			return m, nil
		case "down", "ctrl+n", "ctrl+j":
			m.cursor++
			m.clampCursor()
			m.marqueeOff = 0
			return m, nil
		case "up", "ctrl+p", "ctrl+k":
			m.cursor--
			m.clampCursor()
			m.marqueeOff = 0
			return m, nil
		case "pgdown":
			m.cursor += m.visibleRows()
			m.clampCursor()
			m.marqueeOff = 0
			return m, nil
		case "pgup":
			m.cursor -= m.visibleRows()
			m.clampCursor()
			m.marqueeOff = 0
			return m, nil
		case "home":
			m.cursor = 0
			m.clampCursor()
			m.marqueeOff = 0
			return m, nil
		case "end":
			m.cursor = len(m.filtered) - 1
			m.clampCursor()
			m.marqueeOff = 0
			return m, nil
		}

		// Track the filter value before/after so we can detect mutations and
		// reset the viewport — this is the fix for huh's "press up to see
		// more" bug.
		before := m.input.Value()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if m.input.Value() != before {
			m.applyFilter()
			m.cursor = 0
			m.top = 0
			m.marqueeOff = 0
		}
		return m, cmd
	}
	return m, nil
}

func (m *model) applyFilter() {
	q := strings.TrimSpace(strings.ToLower(m.input.Value()))
	if q == "" {
		m.filtered = append(m.filtered[:0], m.cfg.Items...)
		return
	}
	m.filtered = m.filtered[:0]
	for _, it := range m.cfg.Items {
		hay := strings.ToLower(it.Label + " " + it.Value)
		if fuzzyMatch(hay, q) {
			m.filtered = append(m.filtered, it)
		}
	}
}

// fuzzyMatch returns true if every rune of q appears in hay in order. This is
// the same flavor of match fzf and huh use — "fbar" matches "foobar" or
// "fooBar".
func fuzzyMatch(hay, q string) bool {
	i := 0
	for _, r := range q {
		idx := strings.IndexRune(hay[i:], r)
		if idx < 0 {
			return false
		}
		i += idx + 1
	}
	return true
}

func (m *model) clampCursor() {
	if len(m.filtered) == 0 {
		m.cursor = 0
		m.top = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	visible := m.visibleRows()
	if visible < 1 {
		visible = 1
	}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+visible {
		m.top = m.cursor - visible + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

func (m model) current() (Item, bool) {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return Item{}, false
	}
	return m.filtered[m.cursor], true
}

// boxWidth picks the picker width given the terminal. Cap raised from the
// original 72 → 120 so realistic kubeconfig names (EKS ARNs, GKE FQDNs) stop
// getting cropped mid-name on a normal 120-column window. Rows that *still*
// don't fit at the cap fall back to the marquee scroller in renderRow.
func (m model) boxWidth() int {
	w := m.w - 4
	if w > 120 {
		w = 120
	}
	if w < 24 {
		w = 24
	}
	return w
}

// visibleRows is how many list items fit inside the picker box. We reserve
// rows for: title (1) + filter input (1) + counter line (1) + 2 borders + 2
// vertical padding.
func (m model) visibleRows() int {
	avail := m.h - 8
	if avail < 3 {
		return 3
	}
	maxRows := 14
	if avail > maxRows {
		avail = maxRows
	}
	return avail
}

func (m model) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	w := m.boxWidth()
	visible := m.visibleRows()

	var b strings.Builder
	if m.cfg.Title != "" {
		b.WriteString(titleStyle.Render(m.cfg.Title))
		b.WriteByte('\n')
	}
	b.WriteString(m.input.View())
	b.WriteByte('\n')
	b.WriteString(dividerStyle.Render(strings.Repeat("─", w-2)))
	b.WriteByte('\n')

	if len(m.filtered) == 0 {
		b.WriteString(subtleStyle.Render("  no matches"))
		for i := 1; i < visible; i++ {
			b.WriteByte('\n')
		}
	} else {
		end := m.top + visible
		if end > len(m.filtered) {
			end = len(m.filtered)
		}
		for i := m.top; i < end; i++ {
			it := m.filtered[i]
			selected := i == m.cursor
			b.WriteString(renderRow(it, selected, w-2, m.marqueeOff))
			b.WriteByte('\n')
		}
		// Pad to keep the footer at a stable Y.
		for i := end - m.top; i < visible; i++ {
			b.WriteByte('\n')
		}
	}

	// Counter + arrows
	counter := ""
	if len(m.filtered) > 0 {
		counter = subtleStyle.Render(
			itoa(m.cursor+1) + "/" + itoa(len(m.filtered)),
		)
	}
	help := subtleStyle.Render("enter select · esc cancel · ↑/↓ move")
	pad := w - 2 - lipgloss.Width(counter) - lipgloss.Width(help)
	if pad < 1 {
		pad = 1
	}
	b.WriteString(counter + strings.Repeat(" ", pad) + help)

	box := boxStyle.Width(w).Render(b.String())
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, box)
}

func renderRow(it Item, selected bool, width int, marqueeOff int) string {
	prefix := "  "
	if selected {
		prefix = pointerStyle.Render("▸ ")
	}
	hint := ""
	if it.Hint != "" {
		hint = " " + subtleStyle.Render("("+it.Hint+")")
	}
	prefixW := lipgloss.Width(prefix)
	hintW := lipgloss.Width(hint)
	// Budget for the label proper. prefixW already accounts for the cursor
	// glyph + trailing space; the hint hugs the right of the label.
	labelBudget := width - prefixW - hintW
	if labelBudget < 1 {
		labelBudget = 1
	}

	label := it.Label
	if lipgloss.Width(label) > labelBudget {
		if selected {
			// Selected row: marquee instead of truncating. The user sees the
			// whole name eventually rather than losing the head/tail.
			label = marqueeWindow(it.Label, labelBudget, marqueeOff)
		} else {
			// Non-selected rows: keep static behavior. Truncate from the
			// head so the trailing (usually unique) suffix stays visible.
			runes := []rune(it.Label)
			over := lipgloss.Width(it.Label) - (labelBudget - 1) // room for the ellipsis
			if over > 0 && over < len(runes) {
				label = "…" + string(runes[over:])
			}
		}
	}
	left := prefix + label + hint
	if selected {
		return selectedRowStyle.Render(left)
	}
	return left
}

// marqueeWindow returns a fixed-width slice of label as if it were scrolling
// horizontally with offset `off`. The scrolled text wraps around with
// marqueeSeparator between repeats so the user can tell when one pass ends and
// the next begins. The offset is taken modulo the cycle length so it can grow
// unbounded across ticks without us having to clamp on every event.
//
// We operate on runes, not bytes — context names are usually ASCII but we
// don't want to be wrong for a UTF-8 name.
func marqueeWindow(label string, width, off int) string {
	if label == "" || width <= 0 {
		return ""
	}
	cycle := label + marqueeSeparator
	runes := []rune(cycle)
	n := len(runes)
	off = ((off % n) + n) % n
	out := make([]rune, 0, width)
	for i := 0; i < width; i++ {
		out = append(out, runes[(off+i)%n])
	}
	return string(out)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [11]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("33")).
			Padding(0, 1)

	dividerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	subtleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	pointerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("33")).Bold(true)

	selectedRowStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("33"))

	boxStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("33")).
			Padding(1, 2)
)
