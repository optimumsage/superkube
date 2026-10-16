package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/kubectl"
)

func newApplyCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "apply [-f FILENAME]...",
		Short:              "Apply manifests after previewing the server-side diff",
		Long:               applyLongHelp,
		DisableFlagParsing: true,
		RunE:               runApply,
	}
}

func runApply(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	yes := Flags.Yes || hasFlag(args, "--yes") || hasFlag(args, "-y")
	kubectlArgs := stripFlag(args, "--yes")
	kubectlArgs = stripFlag(kubectlArgs, "-y")

	if err := guardOp("apply", args); err != nil {
		return err
	}

	// --dry-run is parsed here (cobra never sees it: DisableFlagParsing) and
	// removed from the args: `kubectl diff` rejects it, and for the real apply
	// we re-add it explicitly only in the server/client case.
	mode, kubectlArgs, err := extractDryRun(kubectlArgs)
	if err != nil {
		return err
	}

	runner, err := kubectl.New()
	if err != nil {
		return err
	}

	// `-f -` reads the manifest from stdin. Stdin can be consumed only once, so
	// slurp it here and replay the same bytes into both the preview (kubectl
	// diff) and the real apply; otherwise the preview eats it and the apply
	// silently applies nothing.
	var manifest []byte
	fromStdin := usesStdinManifest(kubectlArgs)
	if fromStdin {
		if manifest, err = io.ReadAll(os.Stdin); err != nil {
			return fmt.Errorf("apply: reading manifest from stdin: %w", err)
		}
	}
	stdin := func() io.Reader {
		if fromStdin {
			return bytes.NewReader(manifest)
		}
		return os.Stdin
	}

	return applyFlow(cmd.Context(), runner, mode, kubectlArgs, yes, stdin)
}

// applyFlow runs the dry-run-mode specific apply workflow. mode is one of
// auto|none|server|client (see extractDryRun); stdin returns a fresh reader
// over the manifest for each kubectl invocation.
func applyFlow(ctx context.Context, runner *kubectl.Runner, mode string, args []string, yes bool, stdin func() io.Reader) error {
	switch mode {
	case "server", "client":
		// Non-mutating: behave exactly like kubectl's own dry-run. No diff and
		// no confirmation needed.
		return runner.Run(ctx, append([]string{"apply", "--dry-run=" + mode}, args...),
			kubectl.RunOpts{Stdin: stdin()})
	case "none":
		// Skip the preview but still confirm: this is a real write.
		ReshowBanner()
		if err := confirmApply(yes); err != nil {
			return err
		}
	default: // auto
		preview, err := guardrail.PreviewApplyStdin(ctx, runner, args, stdin(), os.Stderr)
		if err != nil {
			return err
		}
		if !preview.HasChanges {
			return nil
		}
		ReshowBanner()
		if err := confirmApply(yes); err != nil {
			return err
		}
	}
	return runner.Run(ctx, append([]string{"apply"}, args...), kubectl.RunOpts{Stdin: stdin()})
}

func confirmApply(yes bool) error {
	return confirmYesNo(
		"Apply the changes shown above?",
		"You'll see the result from kubectl after confirmation.",
		"APPLY",
		yes,
	)
}

// extractDryRun pulls --dry-run out of args and returns the normalized mode
// (auto|none|server|client) plus the remaining args. Accepts `--dry-run=X`
// and, since superkube's own root flag allows it, `--dry-run X`. A bare
// `--dry-run` means client (kubectl's legacy meaning); true/false map to
// client/none. Absent means auto (preview then confirm).
func extractDryRun(args []string) (mode string, rest []string, err error) {
	mode = "auto"
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		var val string
		switch {
		case a == "--dry-run":
			val = "client"
			if i+1 < len(args) && isDryRunValue(args[i+1]) {
				val = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--dry-run="):
			val = strings.TrimPrefix(a, "--dry-run=")
		default:
			rest = append(rest, a)
			continue
		}
		switch strings.ToLower(val) {
		case "auto", "none", "server", "client":
			mode = strings.ToLower(val)
		case "unchanged", "true":
			mode = "client"
		case "false":
			mode = "none"
		default:
			return "", nil, fmt.Errorf("apply: invalid --dry-run value %q (want auto|none|server|client)", val)
		}
	}
	return mode, rest, nil
}

// isDryRunValue shares guardrail's rule (case-insensitive) so extractDryRun
// and the argv scanners agree on when a bare --dry-run consumes a token.
func isDryRunValue(s string) bool { return guardrail.IsDryRunValue(s) }

// usesStdinManifest reports whether args direct kubectl to read a manifest
// from stdin: `-f -`, `-f=-`, `-f-`, `--filename -`, `--filename=-`.
func usesStdinManifest(args []string) bool {
	for i, a := range args {
		switch a {
		case "-f-", "-f=-", "--filename=-":
			return true
		case "-f", "--filename":
			if i+1 < len(args) && args[i+1] == "-" {
				return true
			}
		}
	}
	return false
}

const applyLongHelp = `Apply manifests with a server-side dry-run preview.

Workflow:
  1. Run kubectl diff to compute the change set vs. live state.
  2. Render the colored diff to your terminal.
  3. Ask for confirmation (Yes / Cancel) — skipped with --yes.
  4. Run the real apply on confirmation.

Notes:
  * If the manifest matches the cluster, no apply happens at all.
  * --dry-run=none skips the preview but still asks for confirmation.
  * --dry-run=server|client runs kubectl's own dry-run apply: nothing is
    changed, so there is no diff and no confirmation.
  * Declining the prompt exits 130.
  * Flags after the verb are passed to kubectl verbatim (-f, -k, --prune, etc).

Example:
  sk apply -f deployment.yaml
  sk apply -k overlays/staging -n web
  sk --yes apply -f -                 # apply stdin without confirming`
