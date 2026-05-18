package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/optimumsage/superkube/internal/ui"
)

// cellColorizer is the per-cell painter for a single column. It receives the
// trimmed cell text and returns a possibly-styled replacement. ANSI escapes
// are zero-width, so column alignment downstream is preserved as long as the
// trailing padding is left untouched.
type cellColorizer func(string) string

// columnSpan is a (start, end) byte range in the header line; rows are sliced
// by the same offsets so we paint the right cell even when column names
// contain a single space (e.g. "LAST SEEN" in `kubectl get events`).
type columnSpan struct {
	name  string
	start int
	end   int // exclusive; end of last column is len(line)
}

// detectColumns finds column spans in a kubectl-style header line. kubectl
// always pads ≥2 spaces between columns, so we split on runs of 2+ whitespace
// to keep multi-word names intact. The last span runs to end-of-line.
func detectColumns(header string) []columnSpan {
	var spans []columnSpan
	n := len(header)
	i := 0
	for i < n {
		// Skip leading spaces.
		for i < n && header[i] == ' ' {
			i++
		}
		if i >= n {
			break
		}
		start := i
		// Read a column name: any non-space chars, plus single spaces that are
		// followed by another non-space (so "LAST SEEN" stays one token).
		for i < n {
			if header[i] == ' ' {
				if i+1 < n && header[i+1] != ' ' {
					i++ // single space inside name
					continue
				}
				break
			}
			i++
		}
		end := i
		spans = append(spans, columnSpan{name: header[start:end], start: start, end: end})
	}
	// Extend each span's end to the start of the next span (capturing the
	// trailing padding into the cell, so we can trim it cleanly).
	for k := 0; k+1 < len(spans); k++ {
		spans[k].end = spans[k+1].start
	}
	if len(spans) > 0 {
		spans[len(spans)-1].end = -1 // sentinel: take to end-of-line
	}
	return spans
}

// colorizerFor returns the per-cell painter for a given column name, or nil
// if the column shouldn't be repainted. Some columns depend on context — for
// nodes, "STATUS" means Ready/NotReady, while for pods it means a phase — so
// the caller passes the inferred resource kind.
func colorizerFor(colName, kind string) cellColorizer {
	switch colName {
	case "STATUS":
		if kind == "node" {
			return ui.ColorizeNodeStatus
		}
		return ui.ColorizeStatus
	case "READY":
		return ui.ColorizeReady
	case "RESTARTS":
		return ui.ColorizeRestarts
	case "AGE":
		return ui.ColorizeAge
	case "LAST SEEN", "FIRST SEEN":
		return ui.ColorizeAge
	case "TYPE":
		if kind == "service" {
			return ui.ColorizeServiceType
		}
		// Events use TYPE for Warning/Normal.
		if kind == "event" {
			return ui.ColorizeEventType
		}
		return nil
	}
	return nil
}

// inferKind returns a short kind hint from a header signature. Used to pick
// the right STATUS / TYPE colorizer without parsing argv. We look at the
// presence of distinctive columns rather than guessing from the resource arg
// (the latter is messy across `pods`, `po`, `pods.v1`, etc.).
func inferKind(headerNames []string) string {
	set := make(map[string]bool, len(headerNames))
	for _, h := range headerNames {
		set[h] = true
	}
	switch {
	// Pods (default and -o wide): always have READY + STATUS + RESTARTS.
	// No other built-in resource exposes all three.
	case set["READY"] && set["STATUS"] && set["RESTARTS"]:
		return "pod"
	case set["READY"] && set["UP-TO-DATE"]:
		return "deployment"
	case set["DESIRED"] && set["CURRENT"] && set["READY"]:
		return "replicaset"
	case set["ROLES"] && set["VERSION"] && set["STATUS"]:
		return "node"
	case set["CLUSTER-IP"] && set["EXTERNAL-IP"]:
		return "service"
	case set["LAST SEEN"] && set["REASON"]:
		return "event"
	case set["HOSTS"] && set["ADDRESS"]:
		return "ingress"
	}
	return ""
}

// rowPainters returns painters adjusted for the contents of a specific row.
// Currently it suppresses the alarmist READY=0/N red on pod rows whose STATUS
// is Completed/Succeeded — a finished job pod legitimately reports 0/1 and
// shouldn't read as a failure. Returns the original painters slice unchanged
// when no override is needed (avoids per-row allocations on the common path).
func rowPainters(line string, spans []columnSpan, painters []cellColorizer, names []string, kind string) []cellColorizer {
	if kind != "pod" {
		return painters
	}
	statusIdx, readyIdx := -1, -1
	for i, n := range names {
		switch n {
		case "STATUS":
			statusIdx = i
		case "READY":
			readyIdx = i
		}
	}
	if statusIdx < 0 || readyIdx < 0 {
		return painters
	}
	status := cellTextAt(line, spans, statusIdx)
	if status != "Completed" && status != "Succeeded" {
		return painters
	}
	out := make([]cellColorizer, len(painters))
	copy(out, painters)
	out[readyIdx] = ui.ColorizeAge // subtle — same muted style we use for AGE
	return out
}

// cellTextAt returns the trimmed text of column `col` from `line`, sliced by
// `spans`. Returns "" if the column is out of range or the line is too short
// for the span's start. Mirrors the slicing paintRow uses, so we read the same
// bytes that get painted.
func cellTextAt(line string, spans []columnSpan, col int) string {
	if col < 0 || col >= len(spans) {
		return ""
	}
	s := spans[col]
	end := s.end
	if end < 0 || end > len(line) {
		end = len(line)
	}
	if s.start > len(line) {
		return ""
	}
	return strings.TrimSpace(line[s.start:end])
}

// paintRow returns a new row line with its cells repainted by `painters`,
// preserving the exact trailing padding for each column (so total visual
// width stays unchanged — ANSI escapes are zero-width).
func paintRow(line string, spans []columnSpan, painters []cellColorizer) string {
	if line == "" {
		return line
	}
	if len(spans) == 0 || len(painters) == 0 {
		return line
	}
	var b strings.Builder
	b.Grow(len(line) + 32)
	for i, sp := range spans {
		start := sp.start
		end := sp.end
		if end < 0 || end > len(line) {
			end = len(line)
		}
		if start > len(line) {
			break
		}
		cell := line[start:end]
		trimmed := strings.TrimRight(cell, " ")
		pad := cell[len(trimmed):]

		if i < len(painters) && painters[i] != nil && trimmed != "" {
			b.WriteString(painters[i](trimmed))
		} else {
			b.WriteString(trimmed)
		}
		b.WriteString(pad)
		// If this is the last span (end sentinel), the cell already runs to
		// end of line; loop will terminate.
	}
	return b.String()
}

// renderGetTable is the colorized writer for kubectl's plain text get output.
// Returns the number of physical lines emitted (used by the watcher for its
// cursor-up redraw math). The header is wrapped with ui.HeaderBg; data rows
// have their known columns repainted in-place.
//
// `kubectl get pods,svc` emits multiple sub-tables separated by a blank line,
// each with its own header — we re-detect spans on every header-shaped line
// that follows a blank or starts the input, so the second sub-table doesn't
// get sliced by the first sub-table's column offsets.
func renderGetTable(w io.Writer, raw string) int {
	if raw == "" {
		return 0
	}
	lines := strings.Split(raw, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return 0
	}

	var (
		spans       []columnSpan
		names       []string
		painters    []cellColorizer
		kind        string
		currentRows []string // captured for the summary footer
		emitted     int
	)
	expectingHeader := true

	flushSummary := func() {
		if ui.Styled() && kind != "" && len(currentRows) > 0 {
			if summary := summarizeRows(kind, names, spans, currentRows); summary != "" {
				fmt.Fprintln(w, ui.Render(ui.Subtle, summary))
				emitted++
			}
		}
		currentRows = nil
	}
	resetTable := func() {
		spans = nil
		names = nil
		painters = nil
		kind = ""
		expectingHeader = true
	}

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			// Blank line: end of the current sub-table. Flush its summary
			// (if any) before printing the separator, so footer order is
			// "header → rows → summary → blank → next-header".
			flushSummary()
			resetTable()
			fmt.Fprintln(w, line)
			emitted++
			continue
		}
		if expectingHeader && looksLikeGetHeader(line) {
			spans = detectColumns(line)
			names = names[:0]
			for _, s := range spans {
				names = append(names, s.name)
			}
			kind = inferKind(names)
			painters = make([]cellColorizer, len(spans))
			for i, s := range spans {
				painters[i] = colorizerFor(s.name, kind)
			}
			fmt.Fprintln(w, ui.Render(ui.HeaderBg, line))
			emitted++
			expectingHeader = false
			continue
		}
		if len(spans) > 0 {
			rp := rowPainters(line, spans, painters, names, kind)
			fmt.Fprintln(w, paintRow(line, spans, rp))
			currentRows = append(currentRows, line)
		} else {
			// No header detected for this chunk — emit verbatim. Keeps us safe
			// when kubectl prints something that doesn't look like a table
			// (warnings, deprecation notices, etc.).
			fmt.Fprintln(w, line)
		}
		emitted++
	}
	flushSummary()
	return emitted
}

// looksLikeGetHeader reports whether line is shaped like a kubectl table
// header: starts at column 0 and whose first whitespace-separated token is
// entirely uppercase letters / digits / a handful of punctuation kubectl uses
// in column names (e.g., NAME, READY, UP-TO-DATE, CLUSTER-IP, PORT(S),
// EXTERNAL-IP). Used to spot the boundary between sub-tables in multi-kind
// `get pods,svc` output without false-positiving on data rows.
func looksLikeGetHeader(line string) bool {
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return false
	}
	first := line
	if sp := strings.IndexByte(line, ' '); sp >= 0 {
		first = line[:sp]
	}
	if first == "" {
		return false
	}
	for _, r := range first {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '(' || r == ')':
		default:
			return false
		}
	}
	return true
}

// summarizeRows produces a one-line footer like "5 pods · 3 Running · 1
// Pending · 1 CrashLoopBackOff" for kinds where a status breakdown is useful.
// Returns "" for kinds without a meaningful breakdown.
func summarizeRows(kind string, headers []string, spans []columnSpan, rows []string) string {
	colIdx := func(name string) int {
		for i, h := range headers {
			if h == name {
				return i
			}
		}
		return -1
	}
	cellAt := func(line string, col int) string {
		if col < 0 || col >= len(spans) {
			return ""
		}
		s := spans[col]
		end := s.end
		if end < 0 || end > len(line) {
			end = len(line)
		}
		if s.start > len(line) {
			return ""
		}
		return strings.TrimSpace(line[s.start:end])
	}

	count := 0
	tally := map[string]int{}
	tallyOrder := []string{}
	bump := func(key string) {
		if key == "" {
			return
		}
		if _, ok := tally[key]; !ok {
			tallyOrder = append(tallyOrder, key)
		}
		tally[key]++
	}

	switch kind {
	case "pod":
		col := colIdx("STATUS")
		if col < 0 {
			return ""
		}
		for _, r := range rows {
			if strings.TrimSpace(r) == "" {
				continue
			}
			count++
			bump(cellAt(r, col))
		}
		if count == 0 {
			return ""
		}
		return formatSummary("pod", count, tallyOrder, tally)
	case "node":
		col := colIdx("STATUS")
		if col < 0 {
			return ""
		}
		for _, r := range rows {
			if strings.TrimSpace(r) == "" {
				continue
			}
			count++
			bump(cellAt(r, col))
		}
		if count == 0 {
			return ""
		}
		return formatSummary("node", count, tallyOrder, tally)
	case "event":
		col := colIdx("TYPE")
		if col < 0 {
			return ""
		}
		for _, r := range rows {
			if strings.TrimSpace(r) == "" {
				continue
			}
			count++
			bump(cellAt(r, col))
		}
		if count == 0 {
			return ""
		}
		return formatSummary("event", count, tallyOrder, tally)
	}
	return ""
}

func formatSummary(singular string, total int, order []string, counts map[string]int) string {
	plural := singular + "s"
	if total == 1 {
		plural = singular
	}
	parts := []string{fmt.Sprintf("%d %s", total, plural)}
	for _, k := range order {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	return strings.Join(parts, " · ")
}
