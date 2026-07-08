// Package ai shells out to the user's local `claude` or `agy` (Antigravity)
// CLI to produce analysis, explanations, and diagnostics. The crucial guarantee
// here is that everything is best-effort and the user is always in control:
//
//   - We never call the provider unless the user explicitly invoked an
//     AI-flavored command (`sk ai`, `sk diagnose`, `sk why`, `sk logs --ai`).
//   - We redact obvious credential-shaped data from the prompt before
//     spawning the child process.
//   - Output is streamed as the model produces it; we never reword or
//     summarize it. On a TTY the caller applies light markdown styling
//     (ui.MarkdownWriter) for readability, but piped/`--plain` output stays
//     raw so scripts see exactly what the model emitted.
package ai

import (
	"context"
	"io"
)

// RunOpts toggles optional capabilities for a single Provider.Run call. The
// zero value means "no tools, no extra capabilities" — the safe default used
// everywhere except when the caller has explicitly asked for more.
type RunOpts struct {
	// AllowReadOnlyKubectl, if true, asks the provider to expose its Bash tool
	// restricted to read-only kubectl/sk verbs. Providers that cannot enforce
	// a per-command allowlist (e.g. antigravity) should treat this as
	// best-effort and rely on the prompt itself to constrain behavior.
	AllowReadOnlyKubectl bool
}

// Provider is the abstraction over the local AI tools we know about. Each
// implementation shells out to a binary on PATH; we never speak HTTP directly.
type Provider interface {
	// Name returns a short label for `sk version` and the audit log.
	Name() string

	// Available reports whether the binary can be located right now. Cached;
	// repeated calls within a process are cheap.
	Available(ctx context.Context) bool

	// VersionString returns a human-readable version line for `sk version`.
	// Best-effort: returns "" if the probe fails.
	VersionString(ctx context.Context) string

	// Run sends prompt to the provider and streams the response into out. The
	// returned error reflects failures of the child process itself; semantic
	// problems with the model's response (e.g., refusal) are part of the
	// stream, not an error. opts is always honored as best-effort; an opt
	// that a particular provider can't enforce is silently ignored.
	Run(ctx context.Context, prompt string, out io.Writer, opts RunOpts) error
}
