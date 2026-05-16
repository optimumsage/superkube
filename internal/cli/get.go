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

func newGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "get RESOURCE [NAME]",
		Short:              "kubectl get with a styled header on TTY (verbatim passthrough otherwise)",
		Long:               getLongHelp,
		DisableFlagParsing: true,
		RunE:               runGet,
	}
}

func runGet(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	runner, err := kubectl.Default()
	if err != nil {
		return err
	}
	full := append([]string{"get"}, args...)

	// Watch mode on a TTY with a default/wide table → render a live-updating
	// frame via client-go. Non-TTY watches still passthrough below so pipes
	// stay byte-for-byte identical for grep/awk/jq users.
	if (hasFlag(args, "-w") || hasFlag(args, "--watch")) && ui.Styled() && !hasNonTableOutput(args) {
		return runGetWatch(cmd, args)
	}

	// Passthrough cases:
	//   - non-TTY (pipe / redirect)  → must stay byte-for-byte identical for
	//                                  grep/awk/jq users
	//   - structured output formats  → we'd corrupt JSON/YAML/etc.
	//   - watch in those modes       → handled by the same passthrough
	if !ui.Styled() || hasNonTableOutput(args) || hasFlag(args, "-w") || hasFlag(args, "--watch") {
		return runner.Run(cmd.Context(), full, kubectl.RunOpts{})
	}

	var stdout bytes.Buffer
	err = runner.Run(cmd.Context(), full, kubectl.RunOpts{
		Stdout: &stdout,
		Stderr: os.Stderr,
	})
	// Even on error (e.g. NotFound) kubectl may still have produced a header.
	// Render whatever we got and propagate the exit code.
	renderGetOutput(cmd.OutOrStdout(), stdout.String())
	return err
}

// hasNonTableOutput reports whether the args request an output format other
// than the default table.
func hasNonTableOutput(args []string) bool {
	for i, a := range args {
		switch {
		case a == "-o" || a == "--output":
			if i+1 < len(args) && !isTableFormat(args[i+1]) {
				return true
			}
		case strings.HasPrefix(a, "-o="):
			if !isTableFormat(strings.TrimPrefix(a, "-o=")) {
				return true
			}
		case strings.HasPrefix(a, "--output="):
			if !isTableFormat(strings.TrimPrefix(a, "--output=")) {
				return true
			}
		}
	}
	return false
}

// isTableFormat reports whether v names a default-style table layout. "wide"
// is a table; "json", "yaml", "name", "jsonpath", "go-template", "custom-columns"
// are not.
func isTableFormat(v string) bool {
	switch v {
	case "", "wide":
		return true
	default:
		return false
	}
}

// renderGetOutput colorizes only the header line, which is the first
// non-empty line of kubectl's table output. Everything else passes through
// unchanged so column alignment stays perfect — kubectl already did the
// formatting work; we just paint the band.
func renderGetOutput(w io.Writer, raw string) {
	if raw == "" {
		return
	}
	first := true
	for line := range splitLines(raw) {
		if first && len(strings.TrimSpace(line)) > 0 {
			fmt.Fprintln(w, ui.Render(ui.HeaderBg, line))
			first = false
			continue
		}
		fmt.Fprintln(w, line)
	}
}

// splitLines yields each line of s without its trailing newline. Allocates
// a slice rather than using bufio so we keep blank lines intact.
func splitLines(s string) <-chan string {
	ch := make(chan string)
	go func() {
		defer close(ch)
		start := 0
		for i := 0; i < len(s); i++ {
			if s[i] == '\n' {
				ch <- s[start:i]
				start = i + 1
			}
		}
		if start < len(s) {
			ch <- s[start:]
		}
	}()
	return ch
}

const getLongHelp = `kubectl get with a colored header band on a TTY.

Behavior:
  * stdout is a TTY and output is table-form        → header rendered with our
                                                       lipgloss style; data rows
                                                       passed through unchanged
                                                       (column alignment preserved).
  * stdout is piped, or -o json|yaml|name|jsonpath
    or --watch is set                               → verbatim passthrough.

Everything else (flags, selectors, namespaces) behaves identically to kubectl.`
