package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/kubectl"
	"github.com/optimumsage/superkube/internal/secretyaml"
	"github.com/optimumsage/superkube/internal/ui"
)

func newSecretCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "secret",
		Aliases: []string{"sec", "secrets"},
		Short:   "View or edit Secrets; values are masked by default",
	}
	c.AddCommand(newSecretViewCmd())
	c.AddCommand(newSecretEditCmd())
	return c
}

func newSecretViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "view NAME",
		Short:              "Show a Secret as YAML; values masked unless --reveal",
		Long:               secretViewLongHelp,
		DisableFlagParsing: true,
		RunE:               runSecretView,
	}
}

func newSecretEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "edit NAME",
		Short:              "Open a Secret in $EDITOR; kubectl applies on save",
		Long:               secretEditLongHelp,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResourceEdit(cmd, args, "secret")
		},
	}
}

func runSecretView(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	reveal := hasFlag(args, "--reveal")
	yes := Flags.Yes || hasFlag(args, "--yes") || hasFlag(args, "-y")
	// Strip superkube-only flags before forwarding to kubectl.
	kubectlArgs := stripFlag(args, "--reveal")
	kubectlArgs = stripFlag(kubectlArgs, "--yes")
	kubectlArgs = stripFlag(kubectlArgs, "-y")

	if len(kubectlArgs) == 0 || firstPositional(kubectlArgs) == "" {
		return errors.New("secret view: NAME is required")
	}

	// Non-TTY --reveal requires explicit --yes. A secret leaking into a logged
	// or recorded session is exactly what this guardrail prevents; the explicit
	// --yes is the user's "I accept" handshake.
	if reveal && !ui.IsStdoutTTY() && !yes {
		return errors.New("secret view --reveal in non-TTY mode requires --yes (refusing to write decoded values to a pipe/file by accident)")
	}

	runner, err := kubectl.Default()
	if err != nil {
		return err
	}

	// Capture stdout so we can transform it before printing. kubectl writes
	// its own errors to stderr; we let those through unchanged.
	var stdout bytes.Buffer
	err = runner.Run(cmd.Context(),
		append([]string{"get", "secret"}, append(kubectlArgs, "-o", "yaml")...),
		kubectl.RunOpts{Stdout: &stdout, Stderr: os.Stderr})
	if err != nil {
		// kubectl already wrote the error; propagate exit code without printing
		// partial / empty captured output.
		return err
	}

	out := stdout.String()
	if reveal {
		out = secretyaml.Reveal(out)
	} else {
		out = secretyaml.Mask(out)
	}
	_, _ = fmt.Fprint(cmd.OutOrStdout(), out)
	return nil
}

const secretViewLongHelp = `Show a Secret as YAML.

By default each value under ` + "`data:`" + ` is replaced with a masking placeholder
so the structure is visible but the bytes aren't leaked into your scrollback,
session recording, or screen-share. Pass ` + "`--reveal`" + ` to base64-decode and
print the real values.

` + "`--reveal`" + ` in non-TTY mode (piping, redirecting to a file) requires
` + "`--yes`" + ` to guard against accidentally writing decoded values where you
didn't expect them. Auditing applies in either mode.`

const secretEditLongHelp = `Edit a Secret in your editor.

Wraps ` + "`kubectl edit secret NAME`" + `, which keeps values base64-encoded
in the buffer. To edit a decoded value, base64-decode before editing and
re-encode before saving (or use a stringData entry, which kubectl encodes
for you on apply). Forbid-policy contexts block edit just like ` + "`delete`" + `.`

// maskSecretYAML is kept for tui.go; the logic lives in internal/secretyaml.
func maskSecretYAML(raw string) string { return secretyaml.Mask(raw) }
