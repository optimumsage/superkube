package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/kubectl"
)

func newConfigmapCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "configmap",
		Aliases: []string{"cm", "configmaps"},
		Short:   "View or edit ConfigMaps with guardrails and audit",
	}
	c.AddCommand(newConfigmapViewCmd())
	c.AddCommand(newConfigmapEditCmd())
	return c
}

func newConfigmapViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "view NAME",
		Short:              "Show a ConfigMap as YAML (colored on TTY)",
		Long:               configmapViewLongHelp,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResourceView(cmd, args, "configmap")
		},
	}
}

func newConfigmapEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "edit NAME",
		Short:              "Open a ConfigMap in $EDITOR; kubectl applies on save",
		Long:               configmapEditLongHelp,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResourceEdit(cmd, args, "configmap")
		},
	}
}

// runResourceView is shared by configmap/ingress view (and secret view, with
// extra masking applied after the bytes come back). It shells out to
// `kubectl get <kind> <name> -n <ns> -o yaml`, prints raw on non-TTY paths,
// and colorizes the first header line on TTY (same band as `sk get`).
func runResourceView(cmd *cobra.Command, args []string, kind string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	if len(args) == 0 || firstPositional(args) == "" {
		return fmt.Errorf("%s view: NAME is required", kind)
	}
	runner, err := kubectl.Default()
	if err != nil {
		return err
	}
	return runner.Run(cmd.Context(),
		append([]string{"get", kind}, append(args, "-o", "yaml")...),
		kubectl.RunOpts{})
}

// runResourceEdit runs `kubectl edit <kind> <name> ...`. kubectl natively
// opens $EDITOR, computes a diff, and applies on save — we just gate it with
// a forbid-policy check and re-show the context banner so the user sees
// where they're editing.
func runResourceEdit(cmd *cobra.Command, args []string, kind string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	if len(args) == 0 || firstPositional(args) == "" {
		return fmt.Errorf("%s edit: NAME is required", kind)
	}
	if rule, blocked := Policy().IsForbidden("edit", append([]string{kind}, args...)); blocked {
		return fmt.Errorf("edit refused by policy %q for context matching %q",
			rule, Policy().MatchedPattern)
	}
	runner, err := kubectl.Default()
	if err != nil {
		return err
	}
	ReshowBanner()
	return runner.Run(cmd.Context(),
		append([]string{"edit", kind}, args...),
		kubectl.RunOpts{})
}

const configmapViewLongHelp = `Show a ConfigMap as YAML.

Behavior is identical to ` + "`kubectl get configmap NAME -o yaml`" + ` — extra
args (e.g. ` + "`-n ns`" + `) are forwarded verbatim. The action is recorded in
the audit log.`

const configmapEditLongHelp = `Edit a ConfigMap in your editor.

Runs ` + "`kubectl edit configmap NAME`" + ` under the hood, so $EDITOR is honored
and kubectl applies the changes on save (or aborts on no-op). Context-level
policy ` + "`forbid:`" + ` rules block edit just like ` + "`delete`" + `.`
