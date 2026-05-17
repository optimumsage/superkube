package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/audit"
	"github.com/optimumsage/superkube/internal/ui"
)

func newAuditCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "audit",
		Short: "Inspect superkube's local audit log",
	}
	c.AddCommand(newAuditLogCmd())
	c.AddCommand(newAuditPathCmd())
	c.AddCommand(newAuditStatsCmd())
	return c
}

// auditFilters bundles the user-facing filter flags so we don't repeat the
// flag-binding boilerplate in every subcommand that needs to scan the log.
type auditFilters struct {
	since   time.Duration
	verb    string
	context string
	failed  bool
	last    int
}

func newAuditLogCmd() *cobra.Command {
	var f auditFilters
	var follow bool
	var raw bool
	c := &cobra.Command{
		Use:   "log",
		Short: "Print audit entries (table on TTY, JSONL when piped / --raw)",
		Long: `Show entries from the local audit log.

By default, output is a compact table on a TTY and raw JSONL when piped or
when --raw is given — this means scripts that already chain ` + "`jq`" + ` keep
working byte-for-byte.

Filters compose: passing both --verb and --failed shows only failed runs of
that verb. --last is applied after the other filters, returning the most
recent N matching entries.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuditLog(cmd, f, follow, raw)
		},
	}
	c.Flags().DurationVar(&f.since, "since", 0, "only show entries newer than this duration (e.g. 1h, 30m)")
	c.Flags().StringVar(&f.verb, "verb", "", "only show entries for this verb (apply, delete, get, ...)")
	c.Flags().StringVar(&f.context, "context", "", "only show entries from this kubectl context (substring match)")
	c.Flags().BoolVar(&f.failed, "failed", false, "only show entries with non-zero exit code")
	c.Flags().IntVar(&f.last, "last", 0, "only show the most recent N matching entries (0 = all)")
	c.Flags().BoolVarP(&follow, "follow", "f", false, "follow new entries as they're written (filters still apply)")
	c.Flags().BoolVar(&raw, "raw", false, "force JSONL output even on a TTY")
	return c
}

func newAuditPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the path to the audit log file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), audit.LogPath())
			return nil
		},
	}
}

func newAuditStatsCmd() *cobra.Command {
	var f auditFilters
	c := &cobra.Command{
		Use:   "stats",
		Short: "Summarize the audit log: counts by verb, context, and exit status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuditStats(cmd, f)
		},
	}
	c.Flags().DurationVar(&f.since, "since", 0, "only consider entries newer than this duration (e.g. 24h)")
	c.Flags().StringVar(&f.verb, "verb", "", "restrict to one verb")
	c.Flags().StringVar(&f.context, "context", "", "restrict to one context (substring match)")
	c.Flags().BoolVar(&f.failed, "failed", false, "restrict to entries with non-zero exit code")
	return c
}

func runAuditLog(cmd *cobra.Command, f auditFilters, follow, raw bool) error {
	out := cmd.OutOrStdout()
	entries, file, pos, err := readAuditFiltered(f)
	if err != nil {
		return err
	}
	if file != nil {
		defer file.Close()
	}

	if len(entries) == 0 && !follow {
		fmt.Fprintln(out, "(no matching audit entries)")
		return nil
	}

	pretty := !raw && ui.Styled() && cmd.OutOrStdout() == os.Stdout
	if pretty {
		renderAuditTable(out, entries)
	} else {
		for _, e := range entries {
			line, _ := json.Marshal(e)
			fmt.Fprintln(out, string(line))
		}
	}

	if !follow {
		return nil
	}
	return followAuditLog(cmd, f, raw, pretty, file, pos)
}

func runAuditStats(cmd *cobra.Command, f auditFilters) error {
	entries, file, _, err := readAuditFiltered(f)
	if err != nil {
		return err
	}
	if file != nil {
		_ = file.Close()
	}

	out := cmd.OutOrStdout()
	if len(entries) == 0 {
		fmt.Fprintln(out, "(no matching audit entries)")
		return nil
	}

	byVerb := map[string]int{}
	byContext := map[string]int{}
	failed := 0
	var totalDur int64
	for _, e := range entries {
		byVerb[e.Verb]++
		ctx := e.Context
		if ctx == "" {
			ctx = "(unset)"
		}
		byContext[ctx]++
		if e.ExitCode != 0 {
			failed++
		}
		totalDur += e.DurationMS
	}
	avgMs := totalDur / int64(len(entries))
	fmt.Fprintf(out, "entries: %d   failed: %d   avg duration: %dms\n\n", len(entries), failed, avgMs)

	fmt.Fprintln(out, "by verb:")
	for _, kv := range topN(byVerb, 10) {
		fmt.Fprintf(out, "  %-12s %d\n", kv.key, kv.n)
	}
	fmt.Fprintln(out, "\nby context:")
	for _, kv := range topN(byContext, 10) {
		fmt.Fprintf(out, "  %-30s %d\n", kv.key, kv.n)
	}
	return nil
}

// readAuditFiltered opens the audit log, applies filters, and returns the
// matching entries. When --last is set we ring-buffer to avoid retaining the
// whole file. The returned *os.File is non-nil only when the caller may want
// to keep reading (i.e. for --follow); pos is the byte offset where reading
// stopped, so followers can pick up cleanly.
func readAuditFiltered(f auditFilters) ([]audit.Event, *os.File, int64, error) {
	file, err := os.Open(audit.LogPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, 0, nil
		}
		return nil, nil, 0, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	cutoff := time.Time{}
	if f.since > 0 {
		cutoff = time.Now().Add(-f.since)
	}

	var ring []audit.Event
	var all []audit.Event
	for scanner.Scan() {
		var e audit.Event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			// Skip malformed lines silently — we'd rather show partial data than
			// fail on a corrupted entry from an older schema.
			continue
		}
		if !matchAuditFilters(e, f, cutoff) {
			continue
		}
		if f.last > 0 {
			ring = append(ring, e)
			if len(ring) > f.last {
				ring = ring[1:]
			}
		} else {
			all = append(all, e)
		}
	}
	if err := scanner.Err(); err != nil {
		_ = file.Close()
		return nil, nil, 0, err
	}
	if f.last > 0 {
		all = ring
	}
	pos, _ := file.Seek(0, io.SeekCurrent)
	return all, file, pos, nil
}

func matchAuditFilters(e audit.Event, f auditFilters, cutoff time.Time) bool {
	if !cutoff.IsZero() && !e.Timestamp.After(cutoff) {
		return false
	}
	if f.verb != "" && e.Verb != f.verb {
		return false
	}
	if f.context != "" && !strings.Contains(e.Context, f.context) {
		return false
	}
	if f.failed && e.ExitCode == 0 {
		return false
	}
	return true
}

// followAuditLog is the streaming counterpart: every 500ms it re-stats the
// file and renders any new matching lines. Best-effort across rotation (we
// just keep reading whatever the open fd points at).
func followAuditLog(cmd *cobra.Command, f auditFilters, raw, pretty bool, file *os.File, pos int64) error {
	if file == nil {
		// Log file didn't exist when we started. Try again periodically.
		for {
			select {
			case <-cmd.Context().Done():
				return nil
			case <-time.After(500 * time.Millisecond):
			}
			ff, err := os.Open(audit.LogPath())
			if err == nil {
				file = ff
				defer file.Close()
				break
			}
		}
	}
	out := cmd.OutOrStdout()
	cutoff := time.Time{}
	if f.since > 0 {
		cutoff = time.Now().Add(-f.since)
	}
	for {
		select {
		case <-cmd.Context().Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
		info, err := os.Stat(audit.LogPath())
		if err != nil || info.Size() <= pos {
			continue
		}
		if _, err := file.Seek(pos, io.SeekStart); err != nil {
			return err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var e audit.Event
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				continue
			}
			if !matchAuditFilters(e, f, cutoff) {
				continue
			}
			if pretty {
				renderAuditTable(out, []audit.Event{e})
			} else {
				line, _ := json.Marshal(e)
				fmt.Fprintln(out, string(line))
			}
		}
		pos = info.Size()
	}
}

// renderAuditTable writes a compact human-readable view: time, verb, context,
// namespace, duration, exit. Falls through PrintTable so colors match the rest
// of the CLI.
func renderAuditTable(w io.Writer, events []audit.Event) {
	headers := []string{"TIME", "VERB", "CONTEXT", "NS", "DUR", "EXIT"}
	rows := make([][]string, 0, len(events))
	for _, e := range events {
		exit := fmt.Sprintf("%d", e.ExitCode)
		if e.ExitCode != 0 {
			exit = ui.Render(ui.Danger, exit)
		} else {
			exit = ui.Render(ui.Success, exit)
		}
		rows = append(rows, []string{
			e.Timestamp.Local().Format("2006-01-02 15:04:05"),
			e.Verb,
			truncate(e.Context, 28),
			truncate(e.Namespace, 14),
			fmt.Sprintf("%dms", e.DurationMS),
			exit,
		})
	}
	ui.PrintTable(w, headers, rows)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return s[:n-1] + "…"
}

type kvPair struct {
	key string
	n   int
}

// topN returns the top-N entries of m sorted by descending count. Stable
// secondary sort by key keeps output deterministic for tests.
func topN(m map[string]int, n int) []kvPair {
	out := make([]kvPair, 0, len(m))
	for k, v := range m {
		out = append(out, kvPair{k, v})
	}
	// Insertion sort — n is tiny (<32 in practice) and we want stable.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			if out[j].n > out[j-1].n || (out[j].n == out[j-1].n && out[j].key < out[j-1].key) {
				out[j], out[j-1] = out[j-1], out[j]
				continue
			}
			break
		}
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}
