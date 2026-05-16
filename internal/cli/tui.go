package cli

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/tui"
	"github.com/optimumsage/superkube/internal/ui"
)

func newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Full-screen pod browser with describe/logs/diagnose actions",
		Long: `Live, full-screen Pods table backed by a client-go informer.

Keys: j/k or arrows to move; / to filter; enter to open the action menu for
the selected pod (describe / logs / diagnose, which suspend the TUI and shell
back into the matching sk subcommand); ? for help; q or ctrl+c to quit.

Honors -n / --namespace for the watched namespace (omit for all namespaces)
and --context for the kubectl context. Refuses to run without a TTY.`,
		RunE: runTUI,
	}
}

func runTUI(cmd *cobra.Command, args []string) error {
	_ = args
	if !ui.Interactive() {
		return errors.New("tui requires an interactive terminal (stdin + stdout TTY); did you redirect or pipe?")
	}
	loader := kube.Loader{KubeconfigPath: Flags.Kubeconfig, Context: Flags.Context}
	cs, err := loader.Clientset()
	if err != nil {
		return err
	}
	// os.Executable resolves the actual binary path so spawned subprocesses
	// invoke the same superkube the user launched, even if it was called via
	// a symlink (the common `sk` case).
	bin, err := os.Executable()
	if err != nil {
		bin = os.Args[0]
	}
	currentCtx, _ := loader.CurrentContext()

	opts := tui.Options{
		Clientset:  cs,
		Namespace:  Flags.Namespace, // empty = all namespaces
		Context:    currentCtx,
		BinaryPath: bin,
		ExtraArgs:  rootFlagsForSubprocess(),
	}
	return tui.Run(cmd.Context(), opts)
}

// rootFlagsForSubprocess captures the root flags that subprocess invocations
// (describe / logs / diagnose) need to inherit, so an action against a pod
// runs against the same kubeconfig/context as the TUI itself.
func rootFlagsForSubprocess() []string {
	var out []string
	if Flags.Kubeconfig != "" {
		out = append(out, "--kubeconfig", Flags.Kubeconfig)
	}
	if Flags.Context != "" {
		out = append(out, "--context", Flags.Context)
	}
	return out
}
