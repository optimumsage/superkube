package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/ui"
	"github.com/optimumsage/superkube/internal/upgrade"
	"github.com/optimumsage/superkube/internal/version"
)

// newUpgradeCmd wires `sk upgrade`. The feature mirrors scripts/install.sh:
// download the latest release tarball, verify its checksum when available,
// and atomically replace the running binary. Confirmation is mandatory in
// interactive contexts; non-interactive callers must pass --yes.
func newUpgradeCmd() *cobra.Command {
	var (
		check         bool
		force         bool
		targetVersion string
	)
	c := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade superkube to the latest release",
		Long: `Upgrade superkube to the latest GitHub release.

Examples:
  sk upgrade                  # check latest, confirm, install
  sk upgrade --check          # just report whether an upgrade is available
  sk upgrade --version v0.2.1 # install a specific version
  sk upgrade --yes            # skip the confirmation prompt
  sk upgrade --force          # reinstall even if already up to date`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			ctx := cmd.Context()

			opt := upgrade.Options{
				CurrentVersion: version.Version,
				TargetVersion:  targetVersion,
				Force:          force,
			}

			plan, err := upgrade.CheckLatest(ctx, opt)
			if err != nil {
				if errors.Is(err, upgrade.ErrUpToDate) {
					fmt.Fprintln(out, "superkube is already up to date ("+version.Version+")")
					return nil
				}
				return err
			}

			fmt.Fprintf(out, "current:  %s\n", displayVersion(plan.CurrentVersion))
			fmt.Fprintf(out, "target:   %s\n", plan.TargetVersion)
			fmt.Fprintf(out, "asset:    %s\n", plan.AssetName)
			fmt.Fprintf(out, "install:  %s\n", plan.BinaryPath)

			if check {
				return nil
			}

			detail := fmt.Sprintf("Install %s over %s?", plan.TargetVersion, plan.BinaryPath)
			if err := guardrail.YesNo("Upgrade superkube", detail, Flags.Yes); err != nil {
				return err
			}

			stop := ui.Spin("upgrading…")
			progress := func(msg string) {
				if Flags.Verbose {
					fmt.Fprintln(os.Stderr, msg)
				}
			}
			runErr := upgrade.Run(ctx, plan, opt, progress)
			stop()
			if runErr != nil {
				return runErr
			}

			fmt.Fprintf(out, "upgraded to %s\n", plan.TargetVersion)
			return nil
		},
	}
	c.Flags().BoolVar(&check, "check", false, "only check whether an upgrade is available")
	c.Flags().BoolVar(&force, "force", false, "reinstall even if already up to date")
	c.Flags().StringVar(&targetVersion, "version", "", "install a specific version (e.g. v0.2.1)")
	return c
}

func displayVersion(v string) string {
	if v == "" {
		return "(unknown)"
	}
	return v
}
