package ui

import (
	"fmt"
	"io"
	"strings"
)

// PrintTable writes a left-aligned table to w. Header colored when Styled().
// Designed for `sk get` output: kubectl-style columns, lipgloss header band.
// Falls back to two-space separators when Plain — kubectl-output-compatible
// for grep/awk users.
func PrintTable(w io.Writer, headers []string, rows [][]string) {
	if len(headers) == 0 && len(rows) == 0 {
		return
	}

	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, c := range row {
			if i >= len(widths) {
				continue
			}
			if l := visualLen(c); l > widths[i] {
				widths[i] = l
			}
		}
	}

	// Header.
	header := formatRow(headers, widths)
	if Styled() {
		header = HeaderBg.Render(header)
	}
	fmt.Fprintln(w, header)

	// Rows.
	for _, row := range rows {
		fmt.Fprintln(w, formatRow(row, widths))
	}
}

func formatRow(cols []string, widths []int) string {
	var b strings.Builder
	for i := 0; i < len(widths); i++ {
		v := ""
		if i < len(cols) {
			v = cols[i]
		}
		pad := widths[i] - visualLen(v)
		if pad < 0 {
			pad = 0
		}
		b.WriteString(v)
		if i < len(widths)-1 {
			b.WriteString(strings.Repeat(" ", pad+2))
		}
	}
	return b.String()
}

// VisualLen returns the printable rune length of s, ignoring ANSI escape
// sequences. Each rune counts as 1; this is good enough for kubectl table
// content (wide CJK characters in resource names are vanishingly rare).
// Exported for callers in internal/cli that need to size columns over
// possibly-styled strings.
func VisualLen(s string) int { return visualLen(s) }

func visualLen(s string) int {
	// Strip basic ANSI CSI sequences just in case future code colorizes cells.
	n := 0
	in := false
	for _, r := range s {
		if r == 0x1b {
			in = true
			continue
		}
		if in {
			if r == 'm' {
				in = false
			}
			continue
		}
		n++
	}
	return n
}
