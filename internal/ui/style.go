package ui

import "github.com/charmbracelet/lipgloss"

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
	if Plain {
		return s
	}
	return style.Render(s)
}
