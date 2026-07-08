package cli

import "time"

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
