package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/kubectl"
)

func newScaleCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "scale --replicas=N TYPE/NAME",
		Short:              "Scale a workload; confirms when scaling to 0",
		DisableFlagParsing: true,
		RunE:               runScale,
	}
}

func runScale(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	if len(args) == 0 {
		return errors.New("scale: --replicas=N and TYPE/NAME required")
	}
	yes := Flags.Yes || boolFlag(args, "yes")
	kubectlArgs := stripSkFlags(args)

	if err := guardOp("scale", args); err != nil {
		return err
	}

	replicas, ok := flagValue(kubectlArgs, "--replicas")
	if !ok {
		return errors.New("scale: --replicas=N is required")
	}
	if replicas == "0" && !isDryRunArgs(kubectlArgs) {
		target := strings.Join(positionalArgs(kubectlArgs), " ")
		ReshowBanner()
		if err := confirmYesNo(
			fmt.Sprintf("Scale %s to zero replicas?", target),
			"This will gracefully terminate all pods of the workload.",
			"SCALE",
			yes,
		); err != nil {
			return err
		}
	}

	runner, err := kubectl.New()
	if err != nil {
		return err
	}
	return runner.Run(cmd.Context(), append([]string{"scale"}, kubectlArgs...), kubectl.RunOpts{})
}
