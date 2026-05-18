package cli

import (
	"github.com/spf13/cobra"
)

func newIngressCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "ingress",
		Aliases: []string{"ing", "ingresses"},
		Short:   "View or edit Ingresses with guardrails and audit",
	}
	c.AddCommand(newIngressViewCmd())
	c.AddCommand(newIngressEditCmd())
	return c
}

func newIngressViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "view NAME",
		Short:              "Show an Ingress as YAML",
		Long:               ingressViewLongHelp,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResourceView(cmd, args, "ingress")
		},
	}
}

func newIngressEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "edit NAME",
		Short:              "Open an Ingress in $EDITOR; kubectl applies on save",
		Long:               ingressEditLongHelp,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResourceEdit(cmd, args, "ingress")
		},
	}
}

const ingressViewLongHelp = `Show an Ingress as YAML.

Identical to ` + "`kubectl get ingress NAME -o yaml`" + ` with audit recorded.`

const ingressEditLongHelp = `Edit an Ingress in your editor.

Wraps ` + "`kubectl edit ingress NAME`" + ` with the same policy gate that
` + "`delete`" + ` uses, so contexts flagged as ` + "`forbid:`" + ` are blocked.`
