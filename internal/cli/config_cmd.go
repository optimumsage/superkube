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
#
# The file is parsed strictly: an unknown key or malformed YAML is reported on
# every run, and guarded commands (delete, apply, scale, rollout, drain,
# cordon, and any forbid-checked passthrough verb) refuse until it is fixed or
# moved away. Read-only commands keep working.
#
# Flags and environment variables always override these settings.

# AI provider and timeout defaults, used when --ai / --ai-timeout and
# $SUPERKUBE_AI are not set. Leave unset to auto-detect (claude > antigravity)
# and use the built-in timeouts (90s, 5m with --tools).
# ai:
#   provider: claude        # claude | antigravity
#   timeout: 90s            # any Go duration: 90s, 2m, ...

# Audit logging. Set enabled: false to turn it off; --audit on overrides this
# for a single run.
# audit:
#   enabled: true

# Guardrails applied to every context.
# guardrails:
#   require_typed_confirm: true   # yes/no confirmations become typed phrases

# Per-context policy. Keys are globs matched against the kubectl context name
# (* = any characters, ? = one character). Every matching entry applies;
# the most specific pattern (exact name, then most literal characters)
# supplies the banner.
# contexts:
#   "prod-*":
#     banner: "PRODUCTION"            # optional banner text
#     require_typed_confirm: true     # typed phrase for every confirmation
#     forbid:                         # blocked even with --yes
#       - "delete --all"              # verb followed by tokens that must all appear
#       - "delete namespace"          # also matches ns / namespaces / namespace/foo
#       - "drain"
#       - "patch"                     # applies to passthrough verbs too
`
