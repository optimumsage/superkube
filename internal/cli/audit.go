package cli

import (
	"bufio"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/audit"
)

func newAuditCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "audit",
		Short: "Inspect superkube's local audit log",
	}
	c.AddCommand(newAuditLogCmd())
	c.AddCommand(newAuditPathCmd())
	return c
}

func newAuditLogCmd() *cobra.Command {
	var since time.Duration
	var follow bool
	c := &cobra.Command{
		Use:   "log",
		Short: "Print the last N audit entries (default: full file)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := os.Open(audit.LogPath())
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "(no audit entries yet)")
					return nil
				}
				return err
			}
			defer f.Close()

			cutoff := time.Time{}
			if since > 0 {
				cutoff = time.Now().Add(-since)
			}
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for scanner.Scan() {
				line := scanner.Text()
				if cutoff.IsZero() || lineAfter(line, cutoff) {
					fmt.Fprintln(cmd.OutOrStdout(), line)
				}
			}
			if err := scanner.Err(); err != nil {
				return err
			}
			if !follow {
				return nil
			}
			// Simple tail-follow: re-stat in a loop. Not robust to rotation
			// across the boundary; good enough for the common case.
			pos, _ := f.Seek(0, 1)
			for {
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(500 * time.Millisecond):
				}
				info, err := os.Stat(audit.LogPath())
				if err != nil {
					continue
				}
				if info.Size() <= pos {
					continue
				}
				if _, err := f.Seek(pos, 0); err != nil {
					return err
				}
				scanner := bufio.NewScanner(f)
				for scanner.Scan() {
					fmt.Fprintln(cmd.OutOrStdout(), scanner.Text())
				}
				pos = info.Size()
			}
		},
	}
	c.Flags().DurationVar(&since, "since", 0, "only show entries newer than this duration (e.g. 1h, 30m)")
	c.Flags().BoolVarP(&follow, "follow", "f", false, "follow new entries as they're written")
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

// lineAfter is a cheap timestamp-prefix check on a JSON-lines record. Returns
// true if we can't parse it (so an unparseable line isn't silently dropped).
func lineAfter(line string, cutoff time.Time) bool {
	// JSON line begins with `{"ts":"2026-...`. Find the value and compare.
	const marker = `"ts":"`
	idx := indexOf(line, marker)
	if idx < 0 {
		return true
	}
	start := idx + len(marker)
	end := start
	for end < len(line) && line[end] != '"' {
		end++
	}
	if end >= len(line) {
		return true
	}
	ts, err := time.Parse(time.RFC3339Nano, line[start:end])
	if err != nil {
		return true
	}
	return ts.After(cutoff)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
