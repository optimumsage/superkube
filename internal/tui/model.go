package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"k8s.io/client-go/kubernetes"
)

// Options bundles the command-line knobs the cli layer passes in. Kept tiny
// because the MVP TUI doesn't expose many switches.
type Options struct {
	Clientset  kubernetes.Interface
	Namespace  string   // empty = all namespaces
	Context    string   // kubectl context name, for the status bar
	BinaryPath string   // os.Executable() resolved at startup
	ExtraArgs  []string // root-flag pass-through for spawned subprocesses (e.g. ["--kubeconfig", "/path"])
}

// Run is the entrypoint called from internal/cli/tui.go.
func Run(ctx context.Context, opts Options) error {
	state := NewState()
	if _, err := StartPodInformer(ctx, opts.Clientset, opts.Namespace, state); err != nil {
		return fmt.Errorf("start informer: %w", err)
	}
	m := newModel(state, opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	return err
}

// --- model ---

type Model struct {
	state *State
	opts  Options

	pods     []PodRow
	filtered []PodRow
	cursor   int
	top      int // viewport top index into filtered
	w, h     int

	filtering  bool
	filter     string
	help       bool
	action     *actionMenu
	statusErr  string
	lastUpdate time.Time
}

type actionMenu struct {
	pod PodRow
}

func newModel(state *State, opts Options) Model {
	return Model{state: state, opts: opts}
}

func (m Model) Init() tea.Cmd { return tickCmd() }

type tickMsg struct{}
type execDoneMsg struct{ err error }

func tickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		m.pods = m.state.Snapshot()
		m.applyFilter()
		m.clampCursor()
		m.lastUpdate = time.Now()
		return m, tickCmd()

	case execDoneMsg:
		// Subprocess returned. Repaint with the latest informer state; the
		// kubeconfig/state may have changed during the suspend.
		m.pods = m.state.Snapshot()
		m.applyFilter()
		if msg.err != nil {
			m.statusErr = msg.err.Error()
		} else {
			m.statusErr = ""
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filtering {
		switch msg.Type {
		case tea.KeyEnter:
			m.filtering = false
			m.applyFilter()
			m.clampCursor()
		case tea.KeyEsc:
			m.filtering = false
			m.filter = ""
			m.applyFilter()
		case tea.KeyBackspace:
			if len(m.filter) > 0 {
				m.filter = m.filter[:len(m.filter)-1]
				m.applyFilter()
			}
		case tea.KeyRunes:
			m.filter += string(msg.Runes)
			m.applyFilter()
		}
		return m, nil
	}

	if m.action != nil {
		return m.handleActionKey(msg)
	}

	switch s := msg.String(); s {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.cursor++
	case "k", "up":
		m.cursor--
	case "g":
		m.cursor = 0
	case "G":
		m.cursor = len(m.filtered) - 1
	case "/":
		m.filtering = true
		m.filter = ""
		m.applyFilter()
	case "?":
		m.help = !m.help
	case "enter":
		if pod, ok := m.currentPod(); ok {
			m.action = &actionMenu{pod: pod}
		}
	}
	m.clampCursor()
	return m, nil
}

func (m Model) handleActionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.action = nil
		return m, nil
	case "a":
		c := m.subprocess("describe", "pod", m.action.pod.Name)
		m.action = nil
		return m, runExternal(c)
	case "l":
		c := m.subprocess("logs", m.action.pod.Name, "-f")
		m.action = nil
		return m, runExternal(c)
	case "d":
		c := m.subprocess("diagnose", "pod/"+m.action.pod.Name)
		m.action = nil
		return m, runExternal(c)
	}
	return m, nil
}

// subprocess builds an *exec.Cmd invoking the same superkube binary with the
// chosen verb. We thread the namespace through explicitly so the action
// inherits the TUI's scope, not whatever the kubeconfig's current-context says.
func (m Model) subprocess(verb string, args ...string) *exec.Cmd {
	argv := []string{}
	if m.action != nil && m.action.pod.Namespace != "" {
		argv = append(argv, "-n", m.action.pod.Namespace)
	}
	argv = append(argv, m.opts.ExtraArgs...)
	argv = append(argv, verb)
	argv = append(argv, args...)
	c := exec.Command(m.opts.BinaryPath, argv...)
	c.Env = os.Environ()
	return c
}

func runExternal(c *exec.Cmd) tea.Cmd {
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return execDoneMsg{err: err}
	})
}

func (m *Model) applyFilter() {
	if m.filter == "" {
		m.filtered = m.pods
		return
	}
	q := strings.ToLower(m.filter)
	out := make([]PodRow, 0, len(m.pods))
	for _, p := range m.pods {
		if strings.Contains(strings.ToLower(p.Name), q) ||
			strings.Contains(strings.ToLower(p.Namespace), q) {
			out = append(out, p)
		}
	}
	m.filtered = out
}

func (m *Model) clampCursor() {
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
		if m.cursor < 0 {
			m.cursor = 0
		}
	}
	// Scroll viewport to keep cursor visible.
	visible := m.tableHeight() - 1 // minus header row
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

func (m Model) currentPod() (PodRow, bool) {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return PodRow{}, false
	}
	return m.filtered[m.cursor], true
}

// tableHeight is the vertical space available for the table area (header + rows).
// Status bar takes 1 line; footer takes 1 line; one blank line between them.
func (m Model) tableHeight() int {
	if m.h <= 4 {
		return 1
	}
	return m.h - 3
}

// --- view ---

var (
	statusStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("238"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("33"))
	subtleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

func (m Model) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.statusBar())
	b.WriteByte('\n')

	if m.action != nil {
		b.WriteString(m.renderActionMenu())
	} else if m.help {
		b.WriteString(m.renderHelp())
	} else {
		b.WriteString(m.renderTable())
	}
	b.WriteByte('\n')
	b.WriteString(m.footer())
	return b.String()
}

func (m Model) statusBar() string {
	ns := m.opts.Namespace
	if ns == "" {
		ns = "(all)"
	}
	left := fmt.Sprintf("superkube tui  ctx: %s  ns: %s  pods: %d",
		shortOrDash(m.opts.Context), ns, len(m.filtered))
	right := ""
	if !m.lastUpdate.IsZero() {
		right = "updated " + m.lastUpdate.Format("15:04:05")
	}
	if m.statusErr != "" {
		right = errorStyle.Render(m.statusErr)
	}
	pad := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return statusStyle.Render(left) + strings.Repeat(" ", pad) + subtleStyle.Render(right)
}

func (m Model) footer() string {
	if m.filtering {
		return subtleStyle.Render("filter: ") + m.filter + subtleStyle.Render("_   (enter:apply  esc:cancel)")
	}
	if m.action != nil {
		return subtleStyle.Render("a:describe  l:logs  d:diagnose  esc:cancel")
	}
	if m.help {
		return subtleStyle.Render("?:close help")
	}
	return subtleStyle.Render("j/k:move  enter:actions  /:filter  ?:help  q:quit")
}

func (m Model) renderTable() string {
	if len(m.filtered) == 0 {
		return subtleStyle.Render("(no pods matched)")
	}
	headers := []string{"NAMESPACE", "NAME", "READY", "STATUS", "RESTARTS", "AGE"}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	visible := m.tableHeight() - 1
	if visible < 1 {
		visible = 1
	}
	end := m.top + visible
	if end > len(m.filtered) {
		end = len(m.filtered)
	}
	for _, r := range m.filtered[m.top:end] {
		widths[0] = max(widths[0], len(r.Namespace))
		widths[1] = max(widths[1], len(r.Name))
		widths[2] = max(widths[2], len(r.Ready))
		widths[3] = max(widths[3], len(r.Phase))
		widths[4] = max(widths[4], len(itoa(int(r.Restarts))))
		widths[5] = max(widths[5], len(humanAgeRow(r.Created)))
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render(formatCells(headers, widths)))
	b.WriteByte('\n')
	for i, r := range m.filtered[m.top:end] {
		row := formatCells([]string{
			r.Namespace, r.Name, r.Ready, r.Phase,
			itoa(int(r.Restarts)), humanAgeRow(r.Created),
		}, widths)
		if m.top+i == m.cursor {
			b.WriteString(cursorStyle.Render(row))
		} else {
			b.WriteString(row)
		}
		b.WriteByte('\n')
	}
	// Pad with blank lines so the footer sits in a stable position when the
	// list is shorter than the viewport.
	for i := end - m.top; i < visible; i++ {
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderActionMenu() string {
	p := m.action.pod
	lines := []string{
		statusStyle.Render(p.Namespace + "/" + p.Name),
		"",
		"  [a] describe",
		"  [l] logs -f",
		"  [d] diagnose (AI)",
		"",
		subtleStyle.Render("  esc to cancel"),
	}
	body := strings.Join(lines, "\n")
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("33")).
		Padding(0, 2).
		Render(body)
	return lipgloss.Place(m.w, m.tableHeight(),
		lipgloss.Center, lipgloss.Center, box)
}

func (m Model) renderHelp() string {
	help := strings.Join([]string{
		statusStyle.Render("superkube tui — keys"),
		"",
		"  j / down       move cursor down",
		"  k / up         move cursor up",
		"  g / G          jump to top / bottom",
		"  /              filter pods by name (esc clears)",
		"  enter          open action menu for the selected pod",
		"  ? / esc        toggle this help",
		"  q / ctrl+c     quit",
	}, "\n")
	return lipgloss.Place(m.w, m.tableHeight(),
		lipgloss.Center, lipgloss.Center, help)
}

// formatCells joins each cell padded to its column width, separated by two
// spaces. Trailing cell isn't padded so the row doesn't have phantom trailing
// whitespace that the cursor highlight would extend over.
func formatCells(cells []string, widths []int) string {
	var sb strings.Builder
	for i, c := range cells {
		if i >= len(widths) {
			break
		}
		sb.WriteString(c)
		if i < len(cells)-1 {
			pad := widths[i] - len(c) + 2
			if pad < 1 {
				pad = 1
			}
			sb.WriteString(strings.Repeat(" ", pad))
		}
	}
	return sb.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var tmp [11]byte
	i := len(tmp)
	for n > 0 {
		i--
		tmp[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		tmp[i] = '-'
	}
	return string(tmp[i:])
}

func humanAgeRow(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func shortOrDash(s string) string {
	if s == "" {
		return "-"
	}
	if len(s) > 40 {
		return "…" + s[len(s)-39:]
	}
	return s
}
