package ui

import (
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

// Palette is the centralized color/style set for superkube. Defined once so the
// look stays consistent across commands. Colors are ANSI 256-friendly; when
// Plain is true, callers should call NoStyle() to get a stripped renderer.
var (
	Title    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	Subtle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	Success  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	Warning  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	Danger   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	Info     = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	Banner   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("33")).Padding(0, 1)
	HeaderBg = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("238"))
)

// Render applies style to s unless Plain is set. Centralizing this means the
// rest of the codebase calls ui.Render(ui.Danger, "...") rather than checking
// Plain at every site.
func Render(style lipgloss.Style, s string) string {
	if Plain || os.Getenv("NO_COLOR") != "" {
		return s
	}
	return style.Render(s)
}

// RenderTo is Render for output headed to a specific writer. Styling is
// emitted only when w is itself a terminal: lipgloss decides colors from
// stdout, so without this check `sk get -w 2>err.log` (stdout a TTY, stderr
// redirected) would write ANSI escapes into the file.
func RenderTo(w io.Writer, style lipgloss.Style, s string) string {
	if !writerIsTTY(w) {
		return s
	}
	return Render(style, s)
}

func writerIsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	if f == os.Stdout {
		return IsStdoutTTY()
	}
	return isatty.IsTerminal(f.Fd())
}
