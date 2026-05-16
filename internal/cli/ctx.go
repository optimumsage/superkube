package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/config"
	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/ui"
	"github.com/optimumsage/superkube/internal/ui/picker"
)

func newCtxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ctx [name|-]",
		Short: "List or switch kubeconfig contexts",
		Long: `Without arguments, prints the available contexts (or opens a fuzzy picker
on a TTY). With a name, switches to that context. With "-", switches back to
the previous context.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runCtx,
	}
	return cmd
}

func runCtx(cmd *cobra.Command, args []string) error {
	loader := kube.Loader{KubeconfigPath: Flags.Kubeconfig, Context: Flags.Context}
	stateDir := config.StateDir()

	contexts, err := loader.ListContexts()
	if err != nil {
		return err
	}
	current, _ := loader.CurrentContext()

	// No arg: interactive picker on a TTY, plain list otherwise.
	if len(args) == 0 {
		if !ui.Interactive() {
			out := cmd.OutOrStdout()
			for _, c := range contexts {
				marker := "  "
				if c == current {
					marker = ui.Render(ui.Success, "* ")
				}
				fmt.Fprintln(out, marker+c)
			}
			return nil
		}
		items := make([]picker.Item, 0, len(contexts))
		for _, c := range contexts {
			it := picker.Item{Label: c, Value: c}
			if c == current {
				it.Hint = "current"
			}
			items = append(items, it)
		}
		picked, ok, err := picker.Run(picker.Config{
			Title:       "Switch context",
			Items:       items,
			Current:     current,
			Placeholder: "filter contexts…",
		})
		if err != nil {
			return err
		}
		if !ok || picked == "" || picked == current {
			return nil
		}
		if err := loader.SwitchContext(picked, stateDir); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), ui.Render(ui.Success, "switched to "+picked))
		return nil
	}

	// One arg.
	target := args[0]
	if target == "-" {
		prev := kube.PreviousContext(stateDir)
		if prev == "" {
			return errors.New("no previous context recorded yet")
		}
		target = prev
	}
	if err := loader.SwitchContext(target, stateDir); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), ui.Render(ui.Success, "switched to "+target))
	return nil
}
