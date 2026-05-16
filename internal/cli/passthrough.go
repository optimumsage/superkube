package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/optimumsage/superkube/internal/kubectl"
)

// runPassthrough forwards the full argv to the user's kubectl binary verbatim.
// This is the linchpin of superkube's "wrapper, not replacement" promise:
// anything we don't have an enhanced command for keeps working because we
// shell out without modification, so krew plugins and kubectl-global flags
// (--v=8, --server, --certificate-authority, etc.) all survive untouched.
//
// We pass the full argv (not verb+rest) so leading kubectl-global flags reach
// kubectl. Superkube-only flags (--ai, --yes, --plain) on an unknown verb will
// cause kubectl to error, which is the correct UX: the user sees that the verb
// isn't a known superkube command and the flags don't apply.
func runPassthrough(ctx context.Context, argv []string) int {
	runner, err := kubectl.Default()
	if err != nil {
		fmt.Fprintln(os.Stderr, "superkube:", err)
		return 127
	}
	if err := runner.Run(ctx, argv, kubectl.RunOpts{}); err != nil {
		var ee *kubectl.ExitCodeError
		if errors.As(err, &ee) {
			return ee.Code
		}
		fmt.Fprintln(os.Stderr, "superkube:", err)
		return 1
	}
	return 0
}
