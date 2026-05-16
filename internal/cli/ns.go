package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/optimumsage/superkube/internal/config"
	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/ui"
)

func newNSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ns [name|-]",
		Short: "List or switch the current namespace",
		Long: `Without arguments, prints the namespaces in the current cluster (or opens a
fuzzy picker on a TTY). With a name, switches to that namespace. With "-",
switches back to the previous namespace. The selection is persisted to your
kubeconfig under the current context, matching kubectx/kubens behavior.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runNS,
	}
	return cmd
}

func runNS(cmd *cobra.Command, args []string) error {
	loader := kube.Loader{KubeconfigPath: Flags.Kubeconfig, Context: Flags.Context}
	stateDir := config.StateDir()
	current, _ := loader.CurrentNamespace()

	// One-arg fast path.
	if len(args) == 1 {
		target := args[0]
		if target == "-" {
			prev := kube.PreviousNamespace(stateDir)
			if prev == "" {
				return errors.New("no previous namespace recorded yet")
			}
			target = prev
		}
		if err := loader.SwitchNamespace(target, stateDir); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), ui.Render(ui.Success, "namespace -> "+target))
		return nil
	}

	// No arg: list namespaces from the cluster.
	names, err := listClusterNamespaces(cmd.Context(), loader)
	if err != nil {
		return err
	}

	if !ui.Interactive() {
		out := cmd.OutOrStdout()
		for _, n := range names {
			marker := "  "
			if n == current {
				marker = ui.Render(ui.Success, "* ")
			}
			fmt.Fprintln(out, marker+n)
		}
		return nil
	}

	picked := current
	options := make([]huh.Option[string], 0, len(names))
	for _, n := range names {
		label := n
		if n == current {
			label = n + " (current)"
		}
		options = append(options, huh.NewOption(label, n))
	}
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Switch namespace").
			Options(options...).
			Value(&picked),
	))
	if err := form.Run(); err != nil {
		return err
	}
	if picked == current {
		return nil
	}
	if err := loader.SwitchNamespace(picked, stateDir); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), ui.Render(ui.Success, "namespace -> "+picked))
	return nil
}

func listClusterNamespaces(ctx context.Context, loader kube.Loader) ([]string, error) {
	cs, err := loader.Clientset()
	if err != nil {
		return nil, err
	}
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stop := ui.Spin("loading namespaces…")
	defer stop()
	nsList, err := cs.CoreV1().Namespaces().List(listCtx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	out := make([]string, 0, len(nsList.Items))
	for _, n := range nsList.Items {
		out = append(out, n.Name)
	}
	sort.Strings(out)
	return out, nil
}
