package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/optimumsage/superkube/internal/ai"
	"github.com/optimumsage/superkube/internal/ui"
)

// Default AI response timeouts. A single-shot prompt is quick, but an agentic
// run (--tools, where the model executes read-only kubectl to investigate) can
// easily exceed 90s, so we grant it more headroom by default.
const (
	defaultAITimeout      = 90 * time.Second
	defaultAIToolsTimeout = 5 * time.Minute
)

// resolveAITimeout picks the context timeout for an AI invocation. An explicit
// --ai-timeout always wins; otherwise the default depends on whether read-only
// tool access is enabled for this run.
func resolveAITimeout(toolsEnabled bool) time.Duration {
	if Flags.AITimeout > 0 {
		return Flags.AITimeout
	}
	if toolsEnabled {
		return defaultAIToolsTimeout
	}
	return defaultAITimeout
}

// streamAIResponse runs the provider and streams its response to stdout,
// styling the markdown line-by-line as it arrives (on a TTY; raw when piped).
// The spinner hides latency to first token, then the styled answer streams in.
// Shared by `sk ai explain`, `sk diagnose`, `sk why`, and `sk logs --ai`.
func streamAIResponse(ctx context.Context, provider ai.Provider, prompt string, opts ai.RunOpts) error {
	md := ui.NewMarkdownWriter(os.Stdout)
	w, stop := ui.SpinUntilFirstByte("asking "+provider.Name()+"…", md)
	defer stop()

	runErr := provider.Run(ctx, prompt, w, opts)
	stop()         // stop the spinner before flushing the trailing line
	_ = md.Flush() // emit any final line the model left without a newline
	if runErr != nil {
		return fmt.Errorf("%s: %w", provider.Name(), runErr)
	}
	fmt.Fprintln(os.Stdout)
	return nil
}
