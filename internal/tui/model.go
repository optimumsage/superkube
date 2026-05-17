package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"k8s.io/client-go/kubernetes"
)

// ProducerFn streams the output of a TUI action into w. ns/name identify the
// target resource. The cli layer constructs these closures so the TUI doesn't
// import the kubectl runner or AI provider directly.
type ProducerFn func(ctx context.Context, ns, name string, w io.Writer) error

// Options bundles the command-line knobs the cli layer passes in.
type Options struct {
	Clientset  kubernetes.Interface
	Namespace  string   // empty = all namespaces
	Context    string   // kubectl context name, for the status bar
	BinaryPath string   // os.Executable() resolved at startup
	ExtraArgs  []string // root-flag pass-through for spawned subprocesses (e.g. ["--kubeconfig", "/path"])

	// Pod-only producers retained from the v0 design — describe / diagnose /
	// why / events run against pods.
	Describe ProducerFn
	Events   ProducerFn
	Diagnose ProducerFn
	Why      ProducerFn

	// YAMLByKind holds the YAML producer per resource kind so `Y` works
	// regardless of which list the user is browsing. The TUI falls back to
	// suspending and shelling out to `sk get <kind> <name> -o yaml` if a
	// kind is missing.
	YAMLByKind map[Kind]ProducerFn
}

// Run is the entrypoint called from internal/cli/tui.go.
func Run(ctx context.Context, opts Options) error {
	state := NewState()
	if _, err := StartInformers(ctx, opts.Clientset, opts.Namespace, state); err != nil {
		return fmt.Errorf("start informers: %w", err)
	}
	m := newModel(ctx, state, opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	return err
}

// view identifies what the user is currently looking at. Only one of these is
// active at a time. Filter input has its own boolean orthogonal to view.
type view int

const (
	viewList view = iota
	viewLogs
	viewText // describe / yaml / events / diagnose / why
	viewHelp
	viewConfirmDelete
)

type Model struct {
	ctx   context.Context
	state *State
	opts  Options

	currentKind Kind
	rows        []Row // for currentKind
	filtered    []Row
	cursor      int
	top         int // viewport top index into filtered
	w, h        int

	view view

	// Filter on resource list. Independent of view so user can filter and
	// then jump into logs / actions on the filtered subset.
	filtering bool
	filter    string

	// Action menu state. Non-nil while menu is visible.
	action *actionMenu

	// Embedded log viewer.
	logs        *logViewer
	logFilterOn bool

	// Embedded text pane (describe / yaml / events / diagnose / why).
	text *textPane

	// Delete confirmation: typed-name flow inside the TUI.
	deleteInput string

	statusErr  string
	lastUpdate time.Time
}

type actionMenu struct {
	row  Row
	kind Kind
}

func newModel(ctx context.Context, state *State, opts Options) Model {
	return Model{ctx: ctx, state: state, opts: opts, view: viewList, currentKind: KindPod}
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
		m.rows = m.state.SnapshotKind(m.currentKind)
		m.applyFilter()
		m.clampCursor()
		m.lastUpdate = time.Now()
		return m, tickCmd()

	case execDoneMsg:
		// Subprocess returned. Repaint with the latest informer state.
		m.rows = m.state.SnapshotKind(m.currentKind)
		m.applyFilter()
		if msg.err != nil {
			m.statusErr = msg.err.Error()
		} else {
			m.statusErr = ""
		}
		return m, nil

	case logLineMsg:
		if m.logs != nil {
			m.logs.append(msg.line)
			return m, m.logs.waitLine()
		}
		return m, nil

	case logEndMsg:
		if m.logs != nil && msg.err != nil && msg.err != context.Canceled {
			m.logs.errMsg = msg.err.Error()
		}
		return m, nil

	case textChunkMsg:
		if m.text != nil {
			m.text.appendChunk(msg.s)
			return m, m.text.waitChunk()
		}
		return m, nil

	case textEndMsg:
		if m.text != nil {
			m.text.done = true
			if msg.err != nil && msg.err != context.Canceled {
				m.text.err = msg.err
			}
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Filter input intercepts most keys when active.
	if m.filtering && m.view == viewList {
		return m.handleFilterKey(msg)
	}
	// Log filter input intercepts when active.
	if m.view == viewLogs && m.logFilterOn {
		return m.handleLogFilterKey(msg)
	}
	if m.view == viewLogs {
		return m.handleLogKey(msg)
	}
	if m.view == viewText {
		if m.text != nil && m.text.filterOn {
			return m.handleTextFilterKey(msg)
		}
		return m.handleTextKey(msg)
	}
	if m.view == viewHelp {
		switch msg.String() {
		case "?", "esc", "q":
			m.view = viewList
		case "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	}
	if m.view == viewConfirmDelete {
		return m.handleDeleteKey(msg)
	}
	if m.action != nil {
		return m.handleActionKey(msg)
	}
	return m.handleListKey(msg)
}

// switchKind changes the active resource family and resets navigation /
// filter state so the user lands at the top of the new list.
func (m *Model) switchKind(k Kind) {
	if m.currentKind == k {
		return
	}
	m.currentKind = k
	m.rows = m.state.SnapshotKind(k)
	m.filter = ""
	m.filtering = false
	m.cursor = 0
	m.top = 0
	m.applyFilter()
	m.clampCursor()
}

func (m Model) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch s := msg.String(); s {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "1":
		m.switchKind(KindPod)
		return m, nil
	case "2":
		m.switchKind(KindConfigMap)
		return m, nil
	case "3":
		m.switchKind(KindSecret)
		return m, nil
	case "4":
		m.switchKind(KindIngress)
		return m, nil
	case "j", "down":
		m.cursor++
	case "k", "up":
		m.cursor--
	case "ctrl+d", "pgdown":
		m.cursor += m.tableHeight() / 2
	case "ctrl+u", "pgup":
		m.cursor -= m.tableHeight() / 2
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = len(m.filtered) - 1
	case "/":
		m.filtering = true
		m.filter = ""
		m.applyFilter()
	case "?":
		m.view = viewHelp
	case "enter":
		if row, ok := m.currentRow(); ok {
			m.action = &actionMenu{row: row, kind: m.currentKind}
		}
	// Shortcut actions that skip the menu — power-user friendly.
	case "l":
		if m.currentKind != KindPod {
			return m, nil
		}
		if pod, ok := m.currentPod(); ok {
			return m, m.openLogs(pod)
		}
	case "d":
		if m.currentKind != KindPod {
			return m, nil
		}
		if pod, ok := m.currentPod(); ok {
			return m, m.openText(pod.Namespace, pod.Name, "describe "+pod.Name, textKindDescribe, m.opts.Describe,
				"describe", "pod", pod.Name)
		}
	case "D":
		if m.currentKind != KindPod {
			return m, nil
		}
		if pod, ok := m.currentPod(); ok {
			return m, m.openText(pod.Namespace, pod.Name, "diagnose "+pod.Name, textKindDiagnose, m.opts.Diagnose,
				"diagnose", "pod/"+pod.Name)
		}
	case "y":
		if m.currentKind != KindPod {
			return m, nil
		}
		if pod, ok := m.currentPod(); ok {
			return m, m.openText(pod.Namespace, pod.Name, "why "+pod.Name, textKindWhy, m.opts.Why,
				"why", "pod/"+pod.Name)
		}
	case "Y":
		if row, ok := m.currentRow(); ok {
			return m, m.openYAML(row)
		}
	case "e":
		if m.currentKind == KindPod {
			return m, nil // edit not exposed for pods in the TUI
		}
		if row, ok := m.currentRow(); ok {
			return m, runExternal(m.subprocessForRow(row, m.currentKind.CLIVerb(), "edit", row.GetName()))
		}
	case "x":
		if m.currentKind != KindPod {
			return m, nil
		}
		if pod, ok := m.currentPod(); ok {
			return m, runExternal(m.subprocessForRow(pod, "exec", "-it", pod.Name, "--", "sh", "-c",
				"command -v bash >/dev/null && exec bash || exec sh"))
		}
	case "X":
		if row, ok := m.currentRow(); ok {
			m.view = viewConfirmDelete
			m.action = &actionMenu{row: row, kind: m.currentKind}
			m.deleteInput = ""
		}
	case "e_pod":
		// Kept for future use — pods are not edited in the TUI directly today.
	}
	m.clampCursor()
	return m, nil
}

func (m Model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		m.filtering = false
		m.applyFilter()
		m.clampCursor()
	case tea.KeyEsc:
		m.filtering = false
		m.filter = ""
		m.applyFilter()
		m.clampCursor()
	case tea.KeyBackspace:
		if len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
			m.applyFilter()
			// Always reset the cursor and viewport when the filter narrows —
			// the user expects to see the top of the new result set.
			m.cursor = 0
			m.top = 0
			m.clampCursor()
		}
	case tea.KeyRunes:
		m.filter += string(msg.Runes)
		m.applyFilter()
		m.cursor = 0
		m.top = 0
		m.clampCursor()
	default:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m Model) handleActionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.action == nil {
		return m, nil
	}
	kind := m.action.kind
	row := m.action.row
	switch msg.String() {
	case "esc", "q":
		m.action = nil
		return m, nil
	case "Y":
		m.action = nil
		return m, m.openYAML(row)
	case "X":
		m.view = viewConfirmDelete
		m.deleteInput = ""
		return m, nil
	}
	// Pod-only actions: everything below.
	if kind != KindPod {
		switch msg.String() {
		case "e":
			m.action = nil
			return m, runExternal(m.subprocessForRow(row, kind.CLIVerb(), "edit", row.GetName()))
		}
		return m, nil
	}
	pod, ok := row.(PodRow)
	if !ok {
		return m, nil
	}
	switch msg.String() {
	case "d":
		m.action = nil
		return m, m.openText(pod.Namespace, pod.Name, "describe "+pod.Name, textKindDescribe, m.opts.Describe,
			"describe", "pod", pod.Name)
	case "l":
		m.action = nil
		return m, m.openLogs(pod)
	case "D":
		m.action = nil
		return m, m.openText(pod.Namespace, pod.Name, "diagnose "+pod.Name, textKindDiagnose, m.opts.Diagnose,
			"diagnose", "pod/"+pod.Name)
	case "y":
		m.action = nil
		return m, m.openText(pod.Namespace, pod.Name, "why "+pod.Name, textKindWhy, m.opts.Why,
			"why", "pod/"+pod.Name)
	case "e":
		m.action = nil
		return m, m.openText(pod.Namespace, pod.Name, "events "+pod.Name, textKindEvents, m.opts.Events,
			"get", "events",
			"--field-selector", "involvedObject.name="+pod.Name,
			"--sort-by=.lastTimestamp")
	case "x":
		c := m.subprocessForRow(pod, "exec", "-it", pod.Name, "--", "sh", "-c",
			"command -v bash >/dev/null && exec bash || exec sh")
		m.action = nil
		return m, runExternal(c)
	}
	return m, nil
}

func (m Model) handleDeleteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.action == nil {
		return m, nil
	}
	row := m.action.row
	kind := m.action.kind
	switch msg.Type {
	case tea.KeyEnter:
		if m.deleteInput == row.GetName() {
			c := m.subprocessForRow(row, "delete", deleteResourceArg(kind), row.GetName())
			m.view = viewList
			m.action = nil
			m.deleteInput = ""
			return m, runExternal(c)
		}
		return m, nil
	case tea.KeyEsc:
		m.view = viewList
		m.action = nil
		m.deleteInput = ""
		return m, nil
	case tea.KeyBackspace:
		if len(m.deleteInput) > 0 {
			m.deleteInput = m.deleteInput[:len(m.deleteInput)-1]
		}
		return m, nil
	case tea.KeyRunes:
		m.deleteInput += string(msg.Runes)
		return m, nil
	default:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

// deleteResourceArg returns the kubectl resource type token that `sk delete`
// expects for kind. Pods use the bare `pod`; for the new kinds the singular
// form maps 1:1.
func deleteResourceArg(kind Kind) string {
	switch kind {
	case KindPod:
		return "pod"
	case KindConfigMap:
		return "configmap"
	case KindSecret:
		return "secret"
	case KindIngress:
		return "ingress"
	}
	return string(kind)
}

func (m Model) handleLogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "b":
		if m.logs != nil {
			m.logs.stop()
		}
		m.logs = nil
		m.view = viewList
		return m, nil
	case "ctrl+c":
		if m.logs != nil {
			m.logs.stop()
		}
		return m, tea.Quit
	case "f":
		if m.logs != nil {
			m.logs.follow = !m.logs.follow
		}
	case "/":
		m.logFilterOn = true
	case "j", "down":
		if m.logs != nil {
			m.logs.offset++
			m.logs.follow = false
		}
	case "k", "up":
		if m.logs != nil {
			m.logs.offset--
			m.logs.follow = false
		}
	case "ctrl+d", "pgdown":
		if m.logs != nil {
			m.logs.offset += m.logViewHeight() / 2
			m.logs.follow = false
		}
	case "ctrl+u", "pgup":
		if m.logs != nil {
			m.logs.offset -= m.logViewHeight() / 2
			m.logs.follow = false
		}
	case "g":
		if m.logs != nil {
			m.logs.offset = 0
			m.logs.follow = false
		}
	case "G":
		if m.logs != nil {
			m.logs.follow = true
		}
	case "c":
		if m.logs != nil {
			m.logs.lines = nil
		}
	}
	return m, nil
}

func (m Model) handleLogFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.logs == nil {
		m.logFilterOn = false
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEnter:
		m.logFilterOn = false
	case tea.KeyEsc:
		m.logFilterOn = false
		m.logs.filter = ""
		m.logs.offset = 0
	case tea.KeyBackspace:
		if len(m.logs.filter) > 0 {
			m.logs.filter = m.logs.filter[:len(m.logs.filter)-1]
			m.logs.offset = 0
		}
	case tea.KeyRunes:
		m.logs.filter += string(msg.Runes)
		m.logs.offset = 0
	default:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

// openText opens the embedded text pane for an action. If prod is non-nil we
// run it as the producer; otherwise we fall back to suspending the TUI and
// shelling out to `sk` with fallbackArgs so the action still works (older
// callers without producers wired up still get the legacy behavior).
func (m *Model) openText(ns, name, title string, kind textKind, prod ProducerFn, fallbackArgs ...string) tea.Cmd {
	if prod == nil {
		c := exec.Command(m.opts.BinaryPath, append(m.subprocessPrefix(ns), append([]string{fallbackArgs[0]}, fallbackArgs[1:]...)...)...)
		c.Env = os.Environ()
		return runExternal(c)
	}
	m.view = viewText
	m.text = newTextPane(title, kind)
	return m.text.start(m.ctx, func(ctx context.Context, w io.Writer) error {
		return prod(ctx, ns, name, w)
	})
}

// openYAML opens the YAML view for any row, choosing the producer registered
// for the current kind. Falls back to suspending the TUI and shelling out to
// `sk get <kind> <name> -o yaml` if no producer was wired.
func (m *Model) openYAML(row Row) tea.Cmd {
	kind := m.currentKind
	prod := m.opts.YAMLByKind[kind]
	title := "yaml " + row.GetName()
	if prod == nil {
		c := exec.Command(m.opts.BinaryPath, append(
			m.subprocessPrefix(row.GetNamespace()),
			"get", deleteResourceArg(kind), row.GetName(), "-o", "yaml",
		)...)
		c.Env = os.Environ()
		return runExternal(c)
	}
	m.view = viewText
	m.text = newTextPane(title, textKindYAML)
	ns, name := row.GetNamespace(), row.GetName()
	return m.text.start(m.ctx, func(ctx context.Context, w io.Writer) error {
		return prod(ctx, ns, name, w)
	})
}

// handleTextKey handles navigation while the text pane is active and the
// filter input is not focused.
func (m Model) handleTextKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "b":
		if m.text != nil {
			m.text.stop()
		}
		m.text = nil
		m.view = viewList
		return m, nil
	case "ctrl+c":
		if m.text != nil {
			m.text.stop()
		}
		return m, tea.Quit
	case "f":
		if m.text != nil {
			m.text.follow = !m.text.follow
		}
	case "/":
		if m.text != nil {
			m.text.filterOn = true
		}
	case "j", "down":
		if m.text != nil {
			m.text.offset++
			m.text.follow = false
		}
	case "k", "up":
		if m.text != nil {
			m.text.offset--
			m.text.follow = false
		}
	case "ctrl+d", "pgdown":
		if m.text != nil {
			m.text.offset += m.logViewHeight() / 2
			m.text.follow = false
		}
	case "ctrl+u", "pgup":
		if m.text != nil {
			m.text.offset -= m.logViewHeight() / 2
			m.text.follow = false
		}
	case "g":
		if m.text != nil {
			m.text.offset = 0
			m.text.follow = false
		}
	case "G":
		if m.text != nil {
			m.text.follow = true
		}
	}
	return m, nil
}

func (m Model) handleTextFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.text == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEnter:
		m.text.filterOn = false
	case tea.KeyEsc:
		m.text.filterOn = false
		m.text.filter = ""
		m.text.offset = 0
	case tea.KeyBackspace:
		if len(m.text.filter) > 0 {
			m.text.filter = m.text.filter[:len(m.text.filter)-1]
			m.text.offset = 0
		}
	case tea.KeyRunes:
		m.text.filter += string(msg.Runes)
		m.text.offset = 0
	default:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

// openLogs spins up a log viewer for pod and returns the tea.Cmd that wires
// the first wait. If the pod has multiple containers we still attach to the
// default — users can re-run `sk logs` with -c if they need a specific one.
func (m *Model) openLogs(pod PodRow) tea.Cmd {
	container := ""
	if len(pod.Containers) > 0 {
		container = pod.Containers[0]
	}
	m.view = viewLogs
	m.logs = newLogViewer(pod.Name, pod.Namespace, container)
	return m.logs.start(m.ctx, m.opts.Clientset)
}

// subprocessPrefix returns the leading argv (namespace + root flags) for a
// `sk <verb>` invocation. We thread the namespace through explicitly so the
// action inherits the row's namespace, not whatever the current-context says.
func (m Model) subprocessPrefix(ns string) []string {
	argv := []string{}
	if ns != "" {
		argv = append(argv, "-n", ns)
	}
	argv = append(argv, m.opts.ExtraArgs...)
	return argv
}

// subprocessForRow builds an *exec.Cmd that invokes the same superkube binary
// with verb (+ args), inheriting the row's namespace.
func (m Model) subprocessForRow(row Row, verb string, args ...string) *exec.Cmd {
	argv := m.subprocessPrefix(row.GetNamespace())
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
		m.filtered = m.rows
		return
	}
	q := strings.ToLower(m.filter)
	out := make([]Row, 0, len(m.rows))
	for _, r := range m.rows {
		for _, s := range r.Filterable() {
			if strings.Contains(strings.ToLower(s), q) {
				out = append(out, r)
				break
			}
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

// currentRow returns the currently-selected Row, or false if there is none.
func (m Model) currentRow() (Row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return nil, false
	}
	return m.filtered[m.cursor], true
}

// currentPod is the legacy pod-only accessor — only valid when the current
// kind is KindPod. Returns false if the selected row isn't a pod or no row
// is selected.
func (m Model) currentPod() (PodRow, bool) {
	r, ok := m.currentRow()
	if !ok {
		return PodRow{}, false
	}
	p, ok := r.(PodRow)
	return p, ok
}

// chromeRows is the number of vertical rows consumed by top status bar (3 lines:
// border-top, content, border-bottom — really we use 1 plus surrounding blank)
// plus the footer (filter line + help line). Computed conservatively so the
// table/log body always has a positive height.
const chromeRows = 5

// tableHeight is the vertical space available for the pod-list table including
// its header row.
func (m Model) tableHeight() int {
	if m.h <= chromeRows+2 {
		return 2
	}
	return m.h - chromeRows
}

// logViewHeight is the vertical space available for the log body (excluding
// the header status row and the help/filter row that the model paints
// alongside the body).
func (m Model) logViewHeight() int {
	if m.h <= chromeRows+3 {
		return 2
	}
	return m.h - chromeRows - 2
}
