package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/ui"
)

// parseWatchArgs decides whether `sk get ... -w` can be served by the
// client-go live table and, if so, builds its options. It supports exactly one
// resource kind (optionally with one name, as `pod foo` or `deploy/web`) plus
// -n/-A/-l/--field-selector/-o wide. Anything else — several kinds
// (`pods,svc`), several names, --show-labels, --sort-by, -L, -f, … — returns
// ok=false and the caller forwards the whole argv to kubectl verbatim, which
// is what the command's doc comment promises for unsupported flags. The
// allowlist is deliberate: silently dropping a flag we don't understand would
// show the user something other than what they asked for.
func parseWatchArgs(args []string) (opts kube.WatchTableOpts, ok bool) {
	var positionals []string
	// valued returns the flag's value for "--name v" / "--name=v" forms.
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, hasEq := strings.Cut(a, "=")
		take := func() (string, bool) {
			if hasEq {
				return val, true
			}
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		if !strings.HasPrefix(a, "-") {
			positionals = append(positionals, a)
			continue
		}
		switch name {
		case "-w", "--watch":
			if hasEq && val != "true" {
				return opts, false
			}
		case "-A", "--all-namespaces":
			opts.AllNamespaces = true
		case "-n", "--namespace":
			v, found := take()
			if !found {
				return opts, false
			}
			opts.Namespace = v
		case "-l", "--selector":
			v, found := take()
			if !found {
				return opts, false
			}
			opts.Selector = v
		case "--field-selector":
			v, found := take()
			if !found {
				return opts, false
			}
			opts.FieldSelector = v
		case "-o", "--output":
			v, found := take()
			if !found || (v != "" && v != "wide") {
				return opts, false
			}
			opts.Wide = v == "wide"
		case "--context", "--kubeconfig":
			// Honored through Flags / effectiveContext; just consume the value.
			if _, found := take(); !found {
				return opts, false
			}
		default:
			return opts, false
		}
	}

	switch len(positionals) {
	case 1:
		res := positionals[0]
		if strings.Contains(res, ",") {
			return opts, false // multiple kinds
		}
		if kind, name, found := strings.Cut(res, "/"); found {
			if kind == "" || name == "" || strings.Contains(name, "/") {
				return opts, false
			}
			opts.Resource, opts.Name = kind, name
		} else {
			opts.Resource = res
		}
	case 2:
		if strings.ContainsAny(positionals[0], ",/") || strings.Contains(positionals[1], "/") {
			return opts, false
		}
		opts.Resource, opts.Name = positionals[0], positionals[1]
	default:
		return opts, false
	}
	if opts.Resource == "" {
		return opts, false
	}
	return opts, true
}

// runGetWatch is the client-go-backed path for `sk get RESOURCE -w`. It opens
// a live watch on the resolved GVR, server-renders Tables, and redraws the
// frame in-place on every change. opts comes from parseWatchArgs; argv shapes
// it can't express never reach here (runGet forwards them to kubectl).
func runGetWatch(cmd *cobra.Command, args []string, opts kube.WatchTableOpts) error {
	if opts.Resource == "" {
		return errors.New("get -w: resource type required (e.g. `sk get pods -w`)")
	}
	if opts.Namespace == "" {
		opts.Namespace = effectiveNamespace(args)
	}

	loader := kube.Loader{
		KubeconfigPath: Flags.Kubeconfig,
		Context:        effectiveContext(args),
	}

	// Reflector logging would write straight into the redrawn table.
	defer kube.QuietClientLogs()()

	fmt.Fprintln(os.Stderr, ui.RenderTo(os.Stderr, ui.Subtle, "watching "+opts.Resource+" — Ctrl-C to stop"))

	var lastLines int
	render := func(f kube.TableFrame) {
		if lastLines > 0 {
			// Cursor-up N lines + clear-to-end-of-screen. This rewrites the
			// previous frame in place so the table appears to "live update"
			// rather than scrolling forever.
			fmt.Fprintf(os.Stdout, "\x1b[%dA\x1b[J", lastLines)
		}
		lastLines = printGetFrame(os.Stdout, f, terminalWidth())
	}

	err := loader.WatchTable(cmd.Context(), opts, render)
	// Trailing newline so the shell prompt lands below the table when the
	// watcher exits (Ctrl-C or apiserver disconnect).
	fmt.Fprintln(os.Stdout)
	return err
}

// terminalWidth returns stdout's column count, or 0 when unknown.
func terminalWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 0
	}
	return w
}

// printGetFrame writes the frame to w in lipgloss-friendly fixed-width form
// and returns the number of lines emitted (used for the cursor-up math). The
// header line is highlighted with ui.HeaderBg; STATUS/READY/RESTARTS/AGE/TYPE
// cells are repainted by ui.Colorize* in place. ANSI escapes are zero-width,
// so the cursor-up redraw math stays correct.
//
// width, when > 0, is the terminal width: every line is truncated to it so no
// row wraps. A wrapped row occupies two physical lines but is counted as one,
// which would leave the cursor-up redraw short by a line per wrapped row and
// stack stale frames. width <= 0 disables truncation.
func printGetFrame(w io.Writer, f kube.TableFrame, width int) int {
	fit := func(line string) string {
		if width > 0 {
			return ansi.Truncate(line, width, "")
		}
		return line
	}
	if len(f.Headers) == 0 {
		fmt.Fprintln(w, fit(ui.Render(ui.Subtle, "(no rows)")))
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
	statusIdx, readyIdx := -1, -1
	for i, name := range headerNames {
		switch name {
		case "STATUS":
			statusIdx = i
		case "READY":
			readyIdx = i
		}
	}

	formatRow := func(cells []string, colorize bool) string {
		// Per-row painter override: for pods that finished (Completed/Succeeded),
		// READY=0/N is expected and shouldn't render as a red failure.
		rowP := painters
		if colorize && kind == "pod" && statusIdx >= 0 && readyIdx >= 0 && statusIdx < len(cells) {
			st := strings.TrimSpace(cells[statusIdx])
			if st == "Completed" || st == "Succeeded" {
				rowP = make([]cellColorizer, len(painters))
				copy(rowP, painters)
				rowP[readyIdx] = ui.ColorizeAge
			}
		}
		var sb strings.Builder
		for i, c := range cells {
			if i >= len(widths) {
				break
			}
			out := c
			if colorize && i < len(rowP) && rowP[i] != nil && c != "" {
				out = rowP[i](c)
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
	fmt.Fprintln(w, fit(ui.Render(ui.HeaderBg, formatRow(f.Headers, false))))
	for _, r := range f.Rows {
		fmt.Fprintln(w, fit(formatRow(r, true)))
	}

	emitted := 1 + len(f.Rows)
	if summary := summarizeFrame(kind, headerNames, f.Rows); summary != "" {
		fmt.Fprintln(w, fit(ui.Render(ui.Subtle, summary)))
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
