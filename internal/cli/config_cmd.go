package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/audit"
	"github.com/optimumsage/superkube/internal/config"
)

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Manage superkube configuration",
	}
	c.AddCommand(newConfigPathCmd())
	c.AddCommand(newConfigInitCmd())
	return c
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print resolved config and state paths",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "config  %s\n", config.ConfigFile())
			fmt.Fprintf(out, "state   %s\n", config.StateDir())
			fmt.Fprintf(out, "cache   %s\n", config.CacheDir())
			fmt.Fprintf(out, "audit   %s\n", audit.LogPath())
			return nil
		},
	}
}

func newConfigInitCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "init",
		Short: "Write a commented default config.yaml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := config.ConfigFile()
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists (pass --force to overwrite)", path)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(defaultConfigYAML), 0o600); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "wrote "+path)
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	return c
}

const defaultConfigYAML = `# superkube configuration
# Most settings can also be overridden via flags or environment variables.

# AI provider preference. Leave blank to auto-detect (claude > antigravity).
# ai:
#   provider: claude        # claude | antigravity
#   timeout: 90s            # also settable per-run via --ai-timeout

# Audit logging. Disable with --audit=off on the command line.
# audit:
#   enabled: true

# Guardrails. v0.1 honors only --yes overrides; richer policy lands in v0.2.
# guardrails:
#   require_typed_confirm: true
#   forbidden_in_contexts:
#     # - "prod-*"
`
