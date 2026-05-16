package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// PrintUnifiedDiff renders a colored unified diff to w. Headers a/b are the
// labels for the "from" and "to" sides (e.g. "live" / "desired"). Falls back
// to plain output when Plain.
func PrintUnifiedDiff(w io.Writer, a, b, fromFile, toFile string) error {
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(a),
		B:        difflib.SplitLines(b),
		FromFile: fromFile,
		ToFile:   toFile,
		Context:  3,
	})
	if err != nil {
		return fmt.Errorf("render diff: %w", err)
	}
	if !Styled() {
		fmt.Fprint(w, diff)
		return nil
	}
	for _, line := range strings.SplitAfter(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			fmt.Fprint(w, Render(Title, line))
		case strings.HasPrefix(line, "@@"):
			fmt.Fprint(w, Render(Info, line))
		case strings.HasPrefix(line, "+"):
			fmt.Fprint(w, Render(Success, line))
		case strings.HasPrefix(line, "-"):
			fmt.Fprint(w, Render(Danger, line))
		default:
			fmt.Fprint(w, line)
		}
	}
	return nil
}
