package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/ui"
)

type ctxCleanFlags struct {
	auto        bool
	manual      bool
	timeout     time.Duration
	concurrency int
	keepOrphans bool
	preview     bool
}

func newCtxCleanCmd() *cobra.Command {
	f := &ctxCleanFlags{}
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Remove stale or unreachable kubeconfig contexts",
		Long: `Remove kubeconfig contexts that you no longer use, or that no longer point
at a reachable cluster. Two modes:

  --manual   (default) Open a fuzzy multi-select picker. Tick the contexts you
             want to remove and confirm; the rest are left alone.
  --auto     Probe every context with a short /version request. Contexts whose
             API server is unreachable (DNS failure, TLS error, connection
             refused, timeout, network unreachable) are queued for removal.
             The list is shown and confirmed before anything is written.

In both modes the active current-context is shown for reference but is never
auto-selected for deletion; you have to opt-in explicitly.

By default, cluster and auth-info entries that become orphaned (no remaining
context references them) are pruned. Pass --keep-orphans to keep them. Pass
--preview to print the list of removals without touching kubeconfig.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCtxClean(cmd, f)
		},
	}
	cmd.Flags().BoolVar(&f.auto, "auto", false, "automatically remove contexts whose API server is unreachable")
	cmd.Flags().BoolVar(&f.manual, "manual", false, "manually pick contexts to remove (default when --auto is unset)")
	cmd.Flags().DurationVar(&f.timeout, "timeout", 4*time.Second, "per-context probe timeout for --auto")
	cmd.Flags().IntVar(&f.concurrency, "concurrency", 8, "how many contexts to probe in parallel for --auto")
	cmd.Flags().BoolVar(&f.keepOrphans, "keep-orphans", false, "do not prune cluster/user entries that become unreferenced")
	cmd.Flags().BoolVar(&f.preview, "preview", false, "show what would be removed without changing kubeconfig")
	return cmd
}

func runCtxClean(cmd *cobra.Command, f *ctxCleanFlags) error {
	if f.auto && f.manual {
		return errors.New("--auto and --manual are mutually exclusive")
	}
	if !f.auto && !f.manual {
		// Default to manual: an auto-prune default would be too surprising.
		f.manual = true
	}

	loader := kube.Loader{KubeconfigPath: Flags.Kubeconfig, Context: Flags.Context}
	contexts, err := loader.ListContexts()
	if err != nil {
		return err
	}
	if len(contexts) == 0 {
		return errors.New("no contexts found in kubeconfig")
	}
	current, _ := loader.CurrentContext()

	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	var toRemove []string
	if f.auto {
		toRemove, err = autoSelectContexts(cmd.Context(), out, loader, contexts, current, f)
	} else {
		toRemove, err = manualSelectContexts(contexts, current)
	}
	if err != nil {
		return err
	}
	if len(toRemove) == 0 {
		fmt.Fprintln(out, ui.Render(ui.Subtle, "nothing to remove."))
		return nil
	}

	fmt.Fprintln(out, ui.Render(ui.Title, fmt.Sprintf("Will remove %d context(s):", len(toRemove))))
	for _, name := range toRemove {
		marker := "  - "
		if name == current {
			marker = ui.Render(ui.Warning, "  ! ") + "(current) "
		}
		fmt.Fprintln(out, marker+name)
	}

	if f.preview {
		fmt.Fprintln(out, ui.Render(ui.Subtle, "preview: kubeconfig not modified."))
		return nil
	}

	prompt := "Remove these contexts from kubeconfig?"
	if containsString(toRemove, current) {
		prompt = "Remove these contexts (INCLUDING the current one)?"
	}
	if err := guardrail.YesNo(prompt, "This rewrites your kubeconfig file.", Flags.Yes); err != nil {
		return err
	}

	removed, failed := 0, 0
	for _, name := range toRemove {
		if err := loader.DeleteContext(name, !f.keepOrphans); err != nil {
			fmt.Fprintln(errOut, ui.Render(ui.Danger, "  ✗ "+name+": "+err.Error()))
			failed++
			continue
		}
		fmt.Fprintln(out, ui.Render(ui.Success, "  ✓ removed "+name))
		removed++
	}
	fmt.Fprintln(out, ui.Render(ui.Subtle, fmt.Sprintf("done: %d removed, %d failed.", removed, failed)))
	if failed > 0 {
		return errSilentFail
	}
	return nil
}

func manualSelectContexts(contexts []string, current string) ([]string, error) {
	if !ui.Interactive() {
		return nil, errors.New("manual cleanup requires a TTY; re-run with --auto or on an interactive terminal")
	}
	options := make([]huh.Option[string], 0, len(contexts))
	for _, c := range contexts {
		label := c
		if c == current {
			label = c + " (current)"
		}
		options = append(options, huh.NewOption(label, c))
	}
	height := len(contexts) + 4
	if height > 18 {
		height = 18
	}
	var selected []string
	form := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("Select contexts to remove").
			Description("Space to toggle · enter to confirm · esc to cancel").
			Options(options...).
			Value(&selected).
			Height(height),
	))
	if err := form.Run(); err != nil {
		// huh returns its own user-cancel error; fold it into our guardrail
		// abort so the exit code stays at 130 instead of 1.
		if strings.Contains(err.Error(), "user aborted") {
			return nil, guardrail.ErrAborted
		}
		return nil, err
	}
	sort.Strings(selected)
	return selected, nil
}

func autoSelectContexts(ctx context.Context, out io.Writer, loader kube.Loader, contexts []string, current string, f *ctxCleanFlags) ([]string, error) {
	concurrency := f.concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	results := make([]kube.ProbeResult, len(contexts))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	stop := ui.Spin(fmt.Sprintf("probing %d contexts…", len(contexts)))
	for i, name := range contexts {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, name string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = kube.ProbeContext(ctx, loader.KubeconfigPath, name, f.timeout)
		}(i, name)
	}
	wg.Wait()
	stop()

	fmt.Fprintln(out, ui.Render(ui.Title, "Probe results:"))
	var bad []string
	for _, r := range results {
		if r.Reachable {
			fmt.Fprintln(out, ui.Render(ui.Success, "  ✓ ")+r.Context+ui.Render(ui.Subtle, "  "+r.Server))
			continue
		}
		fmt.Fprintln(out, ui.Render(ui.Danger, "  ✗ ")+r.Context+"  "+ui.Render(ui.Subtle, briefErr(r.Err)))
		if r.Context == current {
			// Don't auto-prune the current context — a user may have picked it
			// recently; we warn but skip it.
			fmt.Fprintln(out, ui.Render(ui.Warning, "    note: current context is unreachable; skipping. Re-run with --manual to remove."))
			continue
		}
		bad = append(bad, r.Context)
	}
	sort.Strings(bad)
	return bad, nil
}

// briefErr trims a network error message down to its essence so the probe
// table doesn't get blown out by multi-line dial errors.
func briefErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i > 0 {
		msg = msg[:i]
	}
	if len(msg) > 140 {
		msg = msg[:140] + "…"
	}
	return msg
}

func containsString(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}
