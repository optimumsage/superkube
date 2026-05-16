package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// View styles. Kept in one place so the look stays consistent and recoloring
// the TUI is a single-file change.
var (
	colorBrand   = lipgloss.Color("33")  // cyan-blue accent
	colorMuted   = lipgloss.Color("244") // table/help text
	colorSubtle  = lipgloss.Color("240") // borders, dividers
	colorOnDark  = lipgloss.Color("15")  // bright fg on dark bg
	colorSuccess = lipgloss.Color("10")
	colorWarn    = lipgloss.Color("11")
	colorDanger  = lipgloss.Color("9")
	colorInfo    = lipgloss.Color("14")

	styleHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorOnDark).
			Background(colorBrand).
			Padding(0, 1)

	styleStatusBar = lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(colorOnDark).
			Padding(0, 1)

	styleSubtle = lipgloss.NewStyle().Foreground(colorMuted)

	styleColHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorMuted).
			Underline(true)

	styleCursor = lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(colorBrand).
			Bold(true)

	stylePanel = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(colorSubtle).
			Padding(0, 1)

	stylePanelFocus = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(colorBrand).
			Padding(0, 1)

	styleErr = lipgloss.NewStyle().Foreground(colorDanger).Bold(true)

	styleHelpKey = lipgloss.NewStyle().Bold(true).Foreground(colorInfo)
)

// statusColor returns the foreground style for a pod status string. Falls
// back to colorMuted when nothing interesting matches.
func statusColor(status string) lipgloss.Style {
	st := lipgloss.NewStyle()
	switch status {
	case "Running":
		return st.Foreground(colorSuccess)
	case "Pending", "ContainerCreating", "PodInitializing", "Init:0/1", "Init:1/1":
		return st.Foreground(colorWarn)
	case "Succeeded", "Completed":
		return st.Foreground(colorInfo)
	case "Terminating":
		return st.Foreground(lipgloss.Color("208")) // orange
	case "":
		return st.Foreground(colorMuted)
	}
	// Heuristics: anything Crash/Error/BackOff/OOM = danger; anything
	// starting with "Init:" = warn.
	low := strings.ToLower(status)
	switch {
	case strings.Contains(low, "crash"),
		strings.Contains(low, "error"),
		strings.Contains(low, "fail"),
		strings.Contains(low, "backoff"),
		strings.Contains(low, "oomkill"):
		return st.Foreground(colorDanger).Bold(true)
	case strings.HasPrefix(status, "Init:"):
		return st.Foreground(colorWarn)
	}
	return st.Foreground(colorMuted)
}

func (m Model) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	var body string
	switch {
	case m.view == viewHelp:
		body = m.renderHelp()
	case m.view == viewLogs:
		body = m.renderLogs()
	case m.view == viewText:
		body = m.renderText()
	case m.view == viewConfirmDelete:
		body = m.renderDeleteConfirm()
	default:
		body = m.renderListView()
	}

	var b strings.Builder
	b.WriteString(m.titleBar())
	b.WriteByte('\n')
	b.WriteString(m.statusBar())
	b.WriteByte('\n')
	b.WriteString(body)
	b.WriteByte('\n')
	b.WriteString(m.footer())

	// Action menu paints over the screen, anchored to the lower-right so it
	// doesn't obscure the list cursor.
	main := b.String()
	if m.action != nil && m.view == viewList {
		return overlayBottomRight(main, m.renderActionMenu(), m.w, m.h)
	}
	return main
}

// titleBar is the top-most row — a branded header that includes the title and
// (rightside) a tiny clock so users can confirm the TUI is alive.
func (m Model) titleBar() string {
	title := styleHeader.Render(" superkube · tui ")
	right := styleSubtle.Render(time.Now().Format("15:04:05"))
	pad := m.w - lipgloss.Width(title) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return title + strings.Repeat(" ", pad) + right
}

// statusBar is the second line: context, namespace, counts, error if any.
func (m Model) statusBar() string {
	ns := m.opts.Namespace
	if ns == "" {
		ns = "(all)"
	}
	left := fmt.Sprintf("ctx %s  ·  ns %s  ·  pods %d/%d",
		shortOrDash(m.opts.Context), ns, len(m.filtered), len(m.pods))
	right := ""
	if !m.lastUpdate.IsZero() {
		right = "updated " + m.lastUpdate.Format("15:04:05")
	}
	if m.statusErr != "" {
		right = styleErr.Render("err: " + m.statusErr)
	}
	pad := m.w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if pad < 1 {
		pad = 1
	}
	return styleStatusBar.Width(m.w).Render(left + strings.Repeat(" ", pad) + right)
}

// footer is the bottom row: either the filter input (in list view) or a
// context-appropriate keybinding hint.
func (m Model) footer() string {
	switch {
	case m.filtering && m.view == viewList:
		return styleSubtle.Render(" filter: ") + m.filter + cursorPipe() +
			styleSubtle.Render("    enter:apply · esc:clear")
	case m.view == viewLogs && m.logFilterOn:
		return styleSubtle.Render(" log filter: ") + (m.logs.filter) + cursorPipe() +
			styleSubtle.Render("    enter:apply · esc:clear")
	case m.view == viewText && m.text != nil && m.text.filterOn:
		return styleSubtle.Render(" find: ") + m.text.filter + cursorPipe() +
			styleSubtle.Render("    enter:apply · esc:clear")
	case m.view == viewText:
		return helpLine(
			"↑↓/jk", "scroll",
			"g/G", "top/bot",
			"f", "follow",
			"/", "find",
			"b/esc", "back",
			"ctrl+c", "quit",
		)
	case m.view == viewLogs:
		return helpLine(
			"↑↓/jk", "scroll",
			"f", "follow",
			"g/G", "top/bot",
			"/", "filter",
			"c", "clear",
			"b/esc", "back",
			"ctrl+c", "quit",
		)
	case m.view == viewHelp:
		return helpLine("?", "close help", "q/esc", "back")
	case m.action != nil:
		return helpLine(
			"d", "describe",
			"l", "logs",
			"D", "diagnose",
			"y", "why",
			"Y", "yaml",
			"e", "events",
			"x", "exec",
			"X", "delete",
			"esc", "cancel",
		)
	case m.view == viewConfirmDelete:
		return helpLine("type", "pod name", "enter", "confirm", "esc", "cancel")
	default:
		return helpLine(
			"↑↓", "move",
			"/", "filter",
			"enter", "actions",
			"l", "logs",
			"d", "describe",
			"D", "diagnose",
			"y", "why",
			"?", "help",
			"q", "quit",
		)
	}
}

func cursorPipe() string {
	return lipgloss.NewStyle().Foreground(colorBrand).Render("▏")
}

// helpLine formats a list of key/label pairs into a single muted line. We use
// a centered middle dot as separator so it reads at a glance.
func helpLine(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, styleHelpKey.Render(pairs[i])+styleSubtle.Render(" "+pairs[i+1]))
	}
	return " " + strings.Join(parts, styleSubtle.Render("  ·  "))
}

// renderListView paints the main pods table on the left and the details
// sidebar on the right. The split is fixed at ~36% sidebar (28 chars min)
// when there's room, otherwise full-width list.
func (m Model) renderListView() string {
	height := m.tableHeight()
	if height < 3 {
		height = 3
	}
	// Compute split widths. Reserve 2 chars per side for borders.
	sideW := 0
	if m.w >= 100 {
		sideW = m.w / 3
		if sideW < 30 {
			sideW = 30
		}
		if sideW > 48 {
			sideW = 48
		}
	}
	listW := m.w - sideW
	if listW < 20 {
		listW = m.w
		sideW = 0
	}

	list := m.renderTable(listW, height)
	if sideW == 0 {
		return list
	}
	side := m.renderDetails(sideW, height)
	return lipgloss.JoinHorizontal(lipgloss.Top, list, side)
}

func (m Model) renderTable(width, height int) string {
	if len(m.filtered) == 0 {
		body := styleSubtle.Render("  no pods match")
		return stylePanel.Width(width - 2).Height(height - 2).Render(body)
	}
	inner := width - 4
	if inner < 20 {
		inner = 20
	}

	headers := []string{"NAMESPACE", "NAME", "READY", "STATUS", "RESTARTS", "AGE"}
	widths := []int{0, 0, 5, 0, 8, 4}
	for i, h := range headers {
		widths[i] = max(widths[i], len(h))
	}
	visible := height - 4 // panel border (2) + header row (1) + 1 spacer
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
		widths[3] = max(widths[3], len(r.Status))
	}
	// Constrain name column to leave room for the rest.
	other := widths[0] + widths[2] + widths[3] + widths[4] + widths[5] + 10
	if other < inner {
		widths[1] = inner - other
	}

	var b strings.Builder
	headerRow := formatCells(headers, widths, "")
	b.WriteString(styleColHeader.Render(headerRow))
	b.WriteByte('\n')
	for i, r := range m.filtered[m.top:end] {
		row := formatCells([]string{
			r.Namespace, truncate(r.Name, widths[1]), r.Ready, r.Status,
			itoa(int(r.Restarts)), humanAgeRow(r.Created),
		}, widths, r.Status)
		if m.top+i == m.cursor {
			b.WriteString(styleCursor.Width(inner).Render(row))
		} else {
			b.WriteString(row)
		}
		b.WriteByte('\n')
	}
	for i := end - m.top; i < visible; i++ {
		b.WriteByte('\n')
	}
	return stylePanelFocus.Width(width - 2).Height(height - 2).Render(strings.TrimRight(b.String(), "\n"))
}

func (m Model) renderDetails(width, height int) string {
	inner := width - 4
	if inner < 10 {
		inner = 10
	}
	pod, ok := m.currentPod()
	if !ok {
		return stylePanel.Width(width - 2).Height(height - 2).Render(
			styleSubtle.Render("(no pod selected)"))
	}
	rows := []string{
		styleHeader.Render(" details "),
		"",
		detailRow("pod", pod.Name, inner),
		detailRow("namespace", pod.Namespace, inner),
		detailColoredRow("status", pod.Status, statusColor(pod.Status), inner),
		detailRow("ready", pod.Ready, inner),
		detailRow("restarts", itoa(int(pod.Restarts)), inner),
		detailRow("age", humanAgeRow(pod.Created), inner),
		detailRow("node", or(pod.Node, "-"), inner),
		detailRow("ip", or(pod.IP, "-"), inner),
	}
	if len(pod.Containers) > 0 {
		rows = append(rows, "", styleSubtle.Render("containers"))
		for _, c := range pod.Containers {
			rows = append(rows, "  · "+c)
		}
	}
	rows = append(rows, "", styleSubtle.Render("press enter for actions"))
	body := strings.Join(rows, "\n")
	return stylePanel.Width(width - 2).Height(height - 2).Render(body)
}

func detailRow(label, value string, width int) string {
	lab := styleSubtle.Render(label + ":")
	v := truncate(value, width-len(label)-2)
	return lab + " " + v
}

func detailColoredRow(label, value string, vs lipgloss.Style, width int) string {
	lab := styleSubtle.Render(label + ":")
	v := truncate(value, width-len(label)-2)
	return lab + " " + vs.Render(v)
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// renderText paints the embedded text pane (describe/yaml/events/diagnose/why).
// Same chrome as renderLogs — header status line + bordered body — so the user
// has a consistent mental model across action views.
func (m Model) renderText() string {
	height := m.logViewHeight()
	if m.text == nil {
		return stylePanel.Width(m.w - 2).Height(height).Render(styleSubtle.Render("(no content)"))
	}
	header := " " + m.text.statusLine()
	inner := m.w - 4
	if inner < 20 {
		inner = 20
	}
	body := m.text.render(inner, height-2)
	return header + "\n" + stylePanelFocus.Width(m.w-2).Height(height-1).Render(body)
}

// renderLogs paints the embedded log viewer: a header line, the log body in
// a bordered panel, and (when filter is active) the filter caret in the
// footer.
func (m Model) renderLogs() string {
	height := m.logViewHeight()
	if m.logs == nil {
		return stylePanel.Width(m.w - 2).Height(height).Render(styleSubtle.Render("(no log stream)"))
	}
	header := " " + m.logs.statusLine()
	inner := m.w - 4
	if inner < 20 {
		inner = 20
	}
	body := m.logs.render(inner, height-2)
	return header + "\n" + stylePanelFocus.Width(m.w-2).Height(height-1).Render(body)
}

func (m Model) renderHelp() string {
	height := m.tableHeight()
	help := strings.Join([]string{
		styleHeader.Render(" keybindings "),
		"",
		"  " + styleHelpKey.Render("j / ↓") + "       move down",
		"  " + styleHelpKey.Render("k / ↑") + "       move up",
		"  " + styleHelpKey.Render("g / G") + "       jump to top / bottom",
		"  " + styleHelpKey.Render("ctrl+d/u") + "    half-page down/up",
		"  " + styleHelpKey.Render("/") + "           filter pods (name / ns / status)",
		"  " + styleHelpKey.Render("enter") + "       open action menu",
		"",
		styleSubtle.Render("  actions"),
		"  " + styleHelpKey.Render("l") + "           logs (embedded, with filter)",
		"  " + styleHelpKey.Render("d") + "           describe",
		"  " + styleHelpKey.Render("D") + "           diagnose (AI)",
		"  " + styleHelpKey.Render("y") + "           why (AI)",
		"  " + styleHelpKey.Render("Y") + "           yaml",
		"  " + styleHelpKey.Render("e") + "           events",
		"  " + styleHelpKey.Render("x") + "           exec into the pod",
		"  " + styleHelpKey.Render("X") + "           delete (typed-name confirm)",
		"",
		"  " + styleHelpKey.Render("?") + "           toggle this help",
		"  " + styleHelpKey.Render("q / ctrl+c") + "  quit",
	}, "\n")
	return stylePanel.Width(m.w - 2).Height(height).Render(help)
}

func (m Model) renderActionMenu() string {
	if m.action == nil {
		return ""
	}
	p := m.action.pod
	rows := []string{
		styleHeader.Render(" " + p.Namespace + "/" + p.Name + " "),
		"",
		actionRow("l", "logs (in-tui)"),
		actionRow("d", "describe"),
		actionRow("D", "diagnose (AI)"),
		actionRow("y", "why (AI)"),
		actionRow("Y", "yaml"),
		actionRow("e", "events"),
		actionRow("x", "exec"),
		actionRow("X", styleErr.Render("delete")),
		"",
		styleSubtle.Render("  esc to cancel"),
	}
	body := strings.Join(rows, "\n")
	return stylePanelFocus.Padding(0, 2).Render(body)
}

func actionRow(key, label string) string {
	return "  [" + styleHelpKey.Render(key) + "] " + label
}

func (m Model) renderDeleteConfirm() string {
	if m.action == nil {
		return ""
	}
	p := m.action.pod
	height := m.tableHeight()
	rows := []string{
		styleErr.Render(" delete pod "),
		"",
		"  " + p.Namespace + "/" + styleErr.Render(p.Name),
		"",
		"  " + styleSubtle.Render("type the pod name to confirm:"),
		"",
		"    " + m.deleteInput + cursorPipe(),
	}
	body := strings.Join(rows, "\n")
	box := stylePanelFocus.BorderForeground(colorDanger).Render(body)
	return lipgloss.Place(m.w, height, lipgloss.Center, lipgloss.Center, box)
}

// overlayBottomRight composites the menu onto the bottom-right corner of base,
// preserving base elsewhere. lipgloss has no real "compose two strings"
// primitive, so we just place the menu against the right edge with newlines
// for vertical offset.
func overlayBottomRight(base, overlay string, w, h int) string {
	if overlay == "" {
		return base
	}
	ow := lipgloss.Width(overlay)
	oh := lipgloss.Height(overlay)
	if ow >= w || oh >= h {
		// Doesn't fit cleanly — center it in the middle.
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, overlay)
	}
	// Strategy: just place over a translucent layer using lipgloss.Place at
	// bottom-right. The base output beneath is not preserved (that needs
	// per-cell composition we don't want to write), but the menu is the focal
	// element for this brief interaction.
	return lipgloss.Place(w, h, lipgloss.Right, lipgloss.Bottom, overlay)
}

// formatCells joins each cell padded to its column width, separated by two
// spaces. statusColumn applies the per-row color to the STATUS field — the
// 4th column (index 3) in our schema.
func formatCells(cells []string, widths []int, statusFor string) string {
	var sb strings.Builder
	for i, c := range cells {
		if i >= len(widths) {
			break
		}
		cell := c
		if i == 3 && statusFor != "" {
			cell = statusColor(statusFor).Render(c)
		}
		sb.WriteString(cell)
		// Padding is measured against the *unstyled* width.
		raw := c
		if i < len(cells)-1 {
			pad := widths[i] - len(raw) + 2
			if pad < 1 {
				pad = 1
			}
			sb.WriteString(strings.Repeat(" ", pad))
		}
	}
	return sb.String()
}

func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return s[:max-1] + "…"
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
