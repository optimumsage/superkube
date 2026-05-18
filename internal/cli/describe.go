package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/kubectl"
	"github.com/optimumsage/superkube/internal/ui"
)

func newDescribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "describe TYPE[/NAME | -l label]",
		Short:              "kubectl describe with styled section headers and status coloring on TTY",
		Long:               describeLongHelp,
		DisableFlagParsing: true,
		RunE:               runDescribe,
	}
}

func runDescribe(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	runner, err := kubectl.Default()
	if err != nil {
		return err
	}
	full := append([]string{"describe"}, args...)

	// Off-TTY → verbatim so `sk describe pod foo | grep ...` is byte-identical
	// to `kubectl describe pod foo | grep ...`. `kubectl describe` has no -o
	// flag, so we don't gate on output format here — anything weird kubectl
	// emits is left alone by the styler's conservative classifier.
	if !ui.Styled() {
		return runner.Run(cmd.Context(), full, kubectl.RunOpts{})
	}

	var stdout bytes.Buffer
	err = runner.Run(cmd.Context(), full, kubectl.RunOpts{
		Stdout: &stdout,
		Stderr: os.Stderr,
	})
	// Even on a partial failure kubectl may have produced useful output —
	// style what we got and propagate the original exit code.
	renderDescribeOutput(cmd.OutOrStdout(), stdout.String())
	return err
}

// renderDescribeOutput line-by-line styles kubectl describe output. The
// classifier is intentionally conservative — anything we don't confidently
// recognize passes through unchanged, so this stays robust across kubectl
// versions.
//
// Recognized shapes, in switch-case priority order (this MUST match the
// order in the switch below — table-row classification has to win over
// sub-section detection so a "  Type ..." Events header isn't mis-styled):
//   - "Section:" at column 0, ending with ":" and no value     → bold cyan
//   - Inside Events: — header row containing "Type"            → muted
//   - Inside Events: — subsequent non-blank rows               → Type column
//   - Inside Conditions: — header with "Type" and "Status"     → muted
//   - Inside Conditions: — subsequent non-blank rows           → Status col
//   - "  Sub:" indented, ending with ":" and no value          → cyan (info)
//   - Value continuation: indented to / past the previous      → passthrough
//     "Key: Value" line's value column (Tolerations / Annotations
//     wrap onto multiple lines whose pre-colon slice looks like a label,
//     e.g. "node.kubernetes.io/not-ready:NoExecute"; muting those as
//     labels was the previous behavior's biggest visual glitch).
//   - "Label: Value" at any indent, label-shaped               → muted label,
//     status-bearing values colorized (Status/Phase/State/Ready/Restart
//     Count/Reason). Other values stay in the terminal's default color so
//     noisy fields like Container ID and Image references stay readable.
func renderDescribeOutput(w io.Writer, raw string) {
	if raw == "" {
		return
	}
	// Split on '\n', dropping the trailing empty element that strings.Split
	// produces for input ending in a newline (kubectl always does).
	lines := strings.Split(raw, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	// Walk once, tracking which table we're currently inside so we can
	// repaint event Type / condition Status columns. `valueColumn` is the
	// byte column where the most-recent "Key: Value" line's value started;
	// any non-blank follow-up line indented to that column or further is
	// treated as a wrapped continuation of the previous value and passes
	// through unstyled. -1 means "no current key in scope" (after a header,
	// sub-section, or blank line) so the next line is parsed fresh.
	var (
		eventSpans             []columnSpan
		condSpans              []columnSpan
		inEvents, inConditions bool
		valueColumn            = -1
	)

	for _, line := range lines {
		switch {
		case isDescribeSectionHeader(line):
			fmt.Fprintln(w, styleDescribeSection(line))
			lower := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(line), ":"))
			inEvents = lower == "events"
			inConditions = lower == "conditions"
			eventSpans, condSpans = nil, nil
			valueColumn = -1
			continue

		case inEvents && eventSpans == nil && isTableHeaderRow(line, "Type"):
			eventSpans = detectColumnsIndented(line)
			fmt.Fprintln(w, ui.Render(ui.Subtle, line))
			continue
		case inEvents && eventSpans != nil && strings.TrimSpace(line) != "":
			fmt.Fprintln(w, paintIndentedRow(line, eventSpans, eventColorizers(eventSpans)))
			continue

		case inConditions && condSpans == nil && isTableHeaderRow(line, "Type") && isTableHeaderRow(line, "Status"):
			condSpans = detectColumnsIndented(line)
			fmt.Fprintln(w, ui.Render(ui.Subtle, line))
			continue
		case inConditions && condSpans != nil && strings.TrimSpace(line) != "":
			fmt.Fprintln(w, paintIndentedRow(line, condSpans, conditionColorizers(condSpans)))
			continue

		case isDescribeSubSection(line):
			fmt.Fprintln(w, styleDescribeSubSection(line))
			valueColumn = -1
			continue

		default:
			// A blank line ends any current table and any in-flight value.
			if strings.TrimSpace(line) == "" {
				eventSpans, condSpans = nil, nil
				inEvents, inConditions = false, false
				valueColumn = -1
				fmt.Fprintln(w)
				continue
			}
			// Indented to / past the previous value column → continuation.
			// Pass through unchanged so colon-bearing wrap lines like
			// `             node.kubernetes.io/not-ready:NoExecute` don't get
			// their leading slug mis-painted as a key.
			if valueColumn >= 0 && lineIndent(line) >= valueColumn {
				fmt.Fprintln(w, line)
				continue
			}
			styled, nextCol := styleDescribeKeyValue(line)
			fmt.Fprintln(w, styled)
			valueColumn = nextCol
		}
	}
}

// lineIndent returns the count of leading space/tab bytes in line.
func lineIndent(line string) int {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return i
}

// isDescribeSectionHeader matches a top-level (column 0) line that ends with
// ":" and has no value after it. These are the bold section headings kubectl
// emits: "Containers:", "Conditions:", "Events:", "Volumes:", "Tolerations:".
func isDescribeSectionHeader(line string) bool {
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return false
	}
	trimmed := strings.TrimRight(line, " \t")
	if !strings.HasSuffix(trimmed, ":") {
		return false
	}
	// "Key: Value" → not a header (there's content after the colon). Trim the
	// trailing colon and check there's no embedded colon-space.
	body := strings.TrimSuffix(trimmed, ":")
	if strings.Contains(body, ": ") {
		return false
	}
	return true
}

func styleDescribeSection(line string) string {
	return ui.Render(ui.SectionHeader, line)
}

// styleDescribeKeyValue paints "Label: Value" at any indent. The label is
// muted; only status-bearing values get colorized. Other values keep the
// terminal's default color so the eye can read them (gray-on-gray for the
// whole line — as for "Container ID: containerd://…" — used to swallow the
// actual ID, which is worse than no styling).
//
// Returns the styled line and the byte column where the value starts (used
// by the caller to detect wrapped-continuation lines on the next row). When
// the input doesn't look like a real "Label: Value" pair (e.g. JSON blobs
// in annotation continuations, or any line whose pre-colon slice isn't a
// plausible label, or no value follows the colon) the line is returned
// unchanged and the value column is -1.
func styleDescribeKeyValue(line string) (string, int) {
	if line == "" {
		return line, -1
	}
	indent := 0
	for indent < len(line) && (line[indent] == ' ' || line[indent] == '\t') {
		indent++
	}
	rest := line[indent:]
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 {
		return line, -1
	}
	label := rest[:colon]
	if !isPlausibleLabel(label) {
		// Pre-colon text isn't a label-shaped token — e.g., a JSON snippet
		// from an annotation value continuation, or a URL like "containerd://"
		// that happens to be at line start. Leave the line alone.
		return line, -1
	}
	after := rest[colon+1:]
	leadSpaces := 0
	for leadSpaces < len(after) && after[leadSpaces] == ' ' {
		leadSpaces++
	}
	value := after[leadSpaces:]
	if value == "" {
		// "Key:" with no value on this line — caller's sub-section path
		// should have caught this; if not, just mute the label and stop.
		// No useful value column for continuation tracking.
		return line[:indent] + ui.Render(ui.Subtle, label+":") + after[:leadSpaces], -1
	}

	valueCol := indent + colon + 1 + leadSpaces

	painted := value
	switch label {
	case "Status", "Phase", "State":
		painted = ui.ColorizeStatus(value)
	case "Reason":
		// Reason is a free-form string but commonly carries failure semantics
		// (FailedScheduling, FailedMount, BackOff, …); ColorizeStatus is a
		// no-op for non-error values, so this is safe to apply unconditionally.
		painted = ui.ColorizeStatus(value)
	case "Ready":
		painted = ui.ColorizeReady(value)
	case "Restart Count":
		painted = ui.ColorizeRestarts(value)
	}
	// Everything else (Image, Image ID, Container ID, SeccompProfile, IP,
	// Node, Start Time, CreationTimestamp, Annotations, Labels, Service
	// Account, Args, …) keeps the terminal default color. The previous
	// behavior of muting "noisy" fields like Container ID combined poorly
	// with the muted label — both ended up gray and unreadable.
	return line[:indent] + ui.Render(ui.Subtle, label+":") + after[:leadSpaces] + painted, valueCol
}

// isPlausibleLabel reports whether s looks like a kubectl describe field
// label. Labels are letter-led, contain only letters/digits/space/-/_/./( )
// (e.g., "Service Account", "Node-Selectors", "QoS Class", "Last Transition
// Time", or annotation/label keys like "kubectl.kubernetes.io/last-applied"),
// and never start with punctuation. This rejects JSON-like content
// ({"key": ...}) and bare values that happen to have an embedded colon.
func isPlausibleLabel(s string) bool {
	if s == "" || len(s) > 80 {
		return false
	}
	first := s[0]
	if !((first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z')) {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == ' ', c == '-', c == '_', c == '.', c == '/', c == '(', c == ')':
		default:
			return false
		}
	}
	return true
}

// isDescribeSubSection matches an indented line that ends with ":" and has
// no value after it — e.g. a container name like "  app:", a resource block
// label like "  Limits:" / "  Requests:", or a volume name. These are the
// visual anchors *under* a top-level section header, so we paint them in
// regular (non-bold) cyan to distinguish them from the bolded section
// headers above without overpowering them. Requires a plausible label
// shape so JSON snippets / URLs aren't mistaken for headings.
func isDescribeSubSection(line string) bool {
	if line == "" {
		return false
	}
	if line[0] != ' ' && line[0] != '\t' {
		return false
	}
	trimmed := strings.TrimRight(line, " \t")
	if !strings.HasSuffix(trimmed, ":") {
		return false
	}
	body := strings.TrimSuffix(strings.TrimLeft(trimmed, " \t"), ":")
	return isPlausibleLabel(body)
}

func styleDescribeSubSection(line string) string {
	return ui.Render(ui.Info, line)
}

// isTableHeaderRow reports whether line looks like a column-header row that
// contains the given column name (whole word, space-bounded). Used to spot
// the start of the Events / Conditions tables.
func isTableHeaderRow(line, col string) bool {
	if !strings.Contains(line, col) {
		return false
	}
	// All-caps tokens, indented (kubectl indents these tables by 2 spaces).
	trimmed := strings.TrimLeft(line, " ")
	if trimmed == line {
		return false
	}
	for _, r := range trimmed {
		if r == ' ' || r == '\t' {
			continue
		}
		if !(r >= 'A' && r <= 'Z') {
			return false
		}
		break
	}
	return true
}

// detectColumnsIndented is the indented sibling of detectColumns. It walks
// past the leading whitespace, then uses the same multi-word logic.
func detectColumnsIndented(header string) []columnSpan {
	indent := 0
	for indent < len(header) && header[indent] == ' ' {
		indent++
	}
	spans := detectColumns(header[indent:])
	for i := range spans {
		spans[i].start += indent
		if spans[i].end >= 0 {
			spans[i].end += indent
		}
	}
	return spans
}

// eventColorizers returns per-cell painters for an Events: table.
func eventColorizers(spans []columnSpan) []cellColorizer {
	painters := make([]cellColorizer, len(spans))
	for i, s := range spans {
		switch s.name {
		case "Type":
			painters[i] = ui.ColorizeEventType
		case "Age", "First Seen", "Last Seen":
			painters[i] = ui.ColorizeAge
		}
	}
	return painters
}

// conditionColorizers returns per-cell painters for a Conditions: table.
func conditionColorizers(spans []columnSpan) []cellColorizer {
	painters := make([]cellColorizer, len(spans))
	for i, s := range spans {
		switch s.name {
		case "Status":
			painters[i] = ui.ColorizeReady // True/False/Unknown
		case "Last Transition Time", "Age":
			painters[i] = ui.ColorizeAge
		}
	}
	return painters
}

// paintIndentedRow paints an indented table row by repainting the cells
// whose columns have a non-nil painter. paintRow's output starts at the
// first span's byte offset (it doesn't include leading whitespace), so we
// prepend the original indent byte-for-byte.
func paintIndentedRow(line string, spans []columnSpan, painters []cellColorizer) string {
	if len(spans) == 0 {
		return line
	}
	first := spans[0].start
	if first > len(line) {
		return line
	}
	return line[:first] + paintRow(line, spans, painters)
}

const describeLongHelp = `kubectl describe with styled section headers and status coloring on a TTY.

Behavior:
  * stdout is a TTY → section headings ("Containers:", "Conditions:", "Events:",
                       …) are bolded; Status/Phase/Ready values are colorized;
                       Events Warning rows are colored. Output text remains
                       byte-stable apart from injected zero-width ANSI escapes.
  * stdout is piped → verbatim passthrough (so grep/awk pipelines are byte-
                       identical to kubectl describe).

All flags pass through to kubectl unchanged.`
