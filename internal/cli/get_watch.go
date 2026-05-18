package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/ui"
)

// runGetWatch is the client-go-backed path for `sk get RESOURCE -w`. It opens
// a live watch on the resolved GVR, server-renders Tables, and redraws the
// frame in-place on every change. Falls back to runGet's existing kubectl
// passthrough if we can't resolve the resource (CRD discovery hiccup, etc.).
func runGetWatch(cmd *cobra.Command, args []string) error {
	resource := getResourceArg(args)
	if resource == "" {
		return errors.New("get -w: resource type required (e.g. `sk get pods -w`)")
	}

	opts := kube.WatchTableOpts{
		Resource:      resource,
		Namespace:     effectiveNamespace(args),
		AllNamespaces: hasFlag(args, "-A") || hasFlag(args, "--all-namespaces"),
	}
	if sel, ok := flagValue(args, "-l"); ok {
		opts.Selector = sel
	}
	if sel, ok := flagValue(args, "--selector"); ok {
		opts.Selector = sel
	}

	loader := kube.Loader{
		KubeconfigPath: Flags.Kubeconfig,
		Context:        effectiveContext(args),
	}

	fmt.Fprintln(os.Stderr, ui.Render(ui.Subtle, "watching "+resource+" — Ctrl-C to stop"))

	var lastLines int
	render := func(f kube.TableFrame) {
		if lastLines > 0 {
			// Cursor-up N lines + clear-to-end-of-screen. This rewrites the
			// previous frame in place so the table appears to "live update"
			// rather than scrolling forever.
			fmt.Fprintf(os.Stdout, "\x1b[%dA\x1b[J", lastLines)
		}
		lastLines = printGetFrame(os.Stdout, f)
	}

	err := loader.WatchTable(cmd.Context(), opts, render)
	// Trailing newline so the shell prompt lands below the table when the
	// watcher exits (Ctrl-C or apiserver disconnect).
	fmt.Fprintln(os.Stdout)
	return err
}

// printGetFrame writes the frame to w in lipgloss-friendly fixed-width form
// and returns the number of lines emitted (used for the cursor-up math). The
// header line is highlighted with ui.HeaderBg; STATUS/READY/RESTARTS/AGE/TYPE
// cells are repainted by ui.Colorize* in place. ANSI escapes are zero-width,
// so the cursor-up redraw math stays correct.
func printGetFrame(w io.Writer, f kube.TableFrame) int {
	if len(f.Headers) == 0 {
		fmt.Fprintln(w, ui.Render(ui.Subtle, "(no rows)"))
		return 1
	}
	// Width math uses ui.VisualLen so any embedded ANSI in upstream cells
	// (today the TableFrame is plain text, but this keeps us safe if that
	// ever changes) doesn't widen the column past its printable size.
	widths := make([]int, len(f.Headers))
	for i, h := range f.Headers {
		if w := ui.VisualLen(h); w > widths[i] {
			widths[i] = w
		}
	}
	for _, row := range f.Rows {
		for i, c := range row {
			if i >= len(widths) {
				break
			}
			if w := ui.VisualLen(c); w > widths[i] {
				widths[i] = w
			}
		}
	}

	headerNames := make([]string, len(f.Headers))
	copy(headerNames, f.Headers)
	kind := inferKind(headerNames)
	painters := make([]cellColorizer, len(headerNames))
	for i, name := range headerNames {
		painters[i] = colorizerFor(name, kind)
	}

	formatRow := func(cells []string, colorize bool) string {
		var sb strings.Builder
		for i, c := range cells {
			if i >= len(widths) {
				break
			}
			out := c
			if colorize && i < len(painters) && painters[i] != nil && c != "" {
				out = painters[i](c)
			}
			sb.WriteString(out)
			// Pad against the unstyled (visual) width — ANSI from our own
			// colorizer is zero-width on the terminal but len() counts those
			// bytes, which would over-pad.
			pad := widths[i] - ui.VisualLen(c) + 2
			if i == len(cells)-1 {
				pad = 0
			}
			if pad > 0 {
				sb.WriteString(strings.Repeat(" ", pad))
			}
		}
		return sb.String()
	}
	fmt.Fprintln(w, ui.Render(ui.HeaderBg, formatRow(f.Headers, false)))
	for _, r := range f.Rows {
		fmt.Fprintln(w, formatRow(r, true))
	}

	emitted := 1 + len(f.Rows)
	if summary := summarizeFrame(kind, headerNames, f.Rows); summary != "" {
		fmt.Fprintln(w, ui.Render(ui.Subtle, summary))
		emitted++
	}
	return emitted
}

// summarizeFrame builds the same one-line footer as renderGetTable does, but
// for the structured TableFrame the watcher uses. Returns "" for kinds that
// don't have a useful summary breakdown.
func summarizeFrame(kind string, headers []string, rows [][]string) string {
	if kind == "" {
		return ""
	}
	colIdx := func(name string) int {
		for i, h := range headers {
			if h == name {
				return i
			}
		}
		return -1
	}
	cellAt := func(row []string, col int) string {
		if col < 0 || col >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[col])
	}
	tally := map[string]int{}
	order := []string{}
	bump := func(key string) {
		if key == "" {
			return
		}
		if _, ok := tally[key]; !ok {
			order = append(order, key)
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
			bump(cellAt(r, col))
		}
		return formatSummary("pod", len(rows), order, tally)
	case "node":
		col := colIdx("STATUS")
		if col < 0 {
			return ""
		}
		for _, r := range rows {
			bump(cellAt(r, col))
		}
		return formatSummary("node", len(rows), order, tally)
	case "event":
		col := colIdx("TYPE")
		if col < 0 {
			return ""
		}
		for _, r := range rows {
			bump(cellAt(r, col))
		}
		return formatSummary("event", len(rows), order, tally)
	}
	return ""
}

// getResourceArg extracts the kubectl-style resource token from a get argv,
// skipping known value-taking flags so `sk get -n kube-system pods -w` still
// resolves to "pods". The set of value-taking flags is the subset kubectl
// exposes on `get`; unknown flags are treated as boolean (one token).
func getResourceArg(args []string) string {
	valued := map[string]bool{
		"-n": true, "--namespace": true,
		"-l": true, "--selector": true,
		"-o": true, "--output": true,
		"--context":        true,
		"--kubeconfig":     true,
		"--field-selector": true,
		"--server-side":    false,
	}
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && valued[a] {
				skipNext = true
			}
			continue
		}
		return a
	}
	return ""
}
