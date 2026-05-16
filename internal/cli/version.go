package cli

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/ai"
	"github.com/optimumsage/superkube/internal/kubectl"
	"github.com/optimumsage/superkube/internal/version"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print superkube, kubectl, and AI provider versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "superkube  %s\n", version.String())
			fmt.Fprintf(out, "go         %s\n", runtime.Version())
			fmt.Fprintf(out, "platform   %s/%s\n", runtime.GOOS, runtime.GOARCH)
			fmt.Fprintf(out, "kubectl    %s\n", detectKubectlVersion())
			fmt.Fprintf(out, "ai         %s\n", aiProviderVersionString())
			return nil
		},
	}
}

// detectKubectlVersion returns a human-readable string for `sk version`.
// Best-effort: a failed probe yields a helpful note rather than an error,
// because `sk version` should always succeed.
func detectKubectlVersion() string {
	runner, err := kubectl.Default()
	if err != nil {
		return "(not found on PATH)"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, err := runner.Version(ctx)
	if err != nil {
		return fmt.Sprintf("(probe failed: %v)", err)
	}
	if info.GitVersion == "" {
		return "(unknown)"
	}
	if info.Platform != "" {
		return fmt.Sprintf("%s (%s)", info.GitVersion, info.Platform)
	}
	return info.GitVersion
}

// aiProviderVersionString returns a one-line label for `sk version`,
// auto-detecting the preferred provider or honoring --ai.
func aiProviderVersionString() string {
	provider, err := ai.Detect(Flags.AIProvider)
	if err != nil {
		return "(none)"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if v := provider.VersionString(ctx); v != "" {
		return v
	}
	return provider.Name()
}
