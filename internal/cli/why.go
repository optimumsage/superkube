package cli

import (
	"github.com/spf13/cobra"
)

func newWhyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "why TYPE/NAME",
		Short: "Ask the AI provider why a pod is Pending / CrashLoopBackOff / etc.",
		Long: `Same data-gathering as ` + "`sk diagnose`" + ` (describe, events, last 200 log lines)
but with a tighter prompt that enumerates common failure modes
(ImagePullBackOff, CrashLoopBackOff, Pending due to resource pressure, PVC
binding, OOMKilled, probe failure) and asks the model to identify which
applies, citing the specific evidence.

Use this when you know a pod isn't running and want a focused diagnosis.
Use ` + "`sk diagnose`" + ` for open-ended investigation.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAIDiagnostic(cmd, args[0], "why")
		},
	}
}
