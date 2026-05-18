package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/service"
	"github.com/optimumsage/superkube/internal/ui"
)

// newWebUninstallCmd registers `sk web uninstall`, which stops the service
// and removes the launchd/systemd unit file. The sidecar JSON state is
// removed as well. Uninstall does not touch the log files — users may want
// to inspect them after teardown.
func newWebUninstallCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the web UI background service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, err := service.NewManager()
			if err != nil {
				return err
			}
			label := webServiceLabelForPlatform()
			st, err := mgr.Status(label)
			if err != nil {
				return err
			}
			if !st.Installed {
				fmt.Fprintln(cmd.OutOrStdout(), ui.Render(ui.Subtle, "no web service installed; nothing to do"))
				_ = removeWebServiceState()
				return nil
			}
			if err := guardrail.YesNo("Uninstall sk web service?", "stops the running service and removes the unit file", Flags.Yes); err != nil {
				return err
			}
			if err := mgr.Uninstall(label); err != nil && !errors.Is(err, service.ErrNotInstalled) {
				return err
			}
			if err := removeWebServiceState(); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), ui.Render(ui.Warning, "warning: failed to remove web service state file: "+err.Error()))
			}
			fmt.Fprintln(cmd.OutOrStdout(), ui.Render(ui.Success, "uninstalled web service: "+label))
			return nil
		},
	}
	return c
}
