// Package cli wires the cobra command tree. internal/cli/root.go is the
// entrypoint: it defines persistent flags shared across all commands and the
// pre/post-run middleware that applies guardrails and writes audit events.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/audit"
	"github.com/optimumsage/superkube/internal/config"
	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/kubectl"
	"github.com/optimumsage/superkube/internal/ui"
	"github.com/optimumsage/superkube/internal/version"
)

// PersistentFlags holds the values of the root-level flags. Package-global so
// subcommands can read them without threading state through every signature.
type PersistentFlags struct {
	Context    string
	Namespace  string
	Kubeconfig string
	AIProvider string
	Yes        bool
	DryRun     string
	Plain      bool
	Verbose    bool
	Audit      string
	NoContext  bool
}

// Flags is the parsed root flag set. Populated by cobra before any RunE fires.
var Flags = &PersistentFlags{}

// Execute is the main entrypoint called from cmd/superkube/main.go. It returns
// the process exit code so main can pass it straight to os.Exit.
func Execute(args []string) int {
	root := NewRootCmd()
	start := time.Now()

	// If the first positional token is an unknown verb, route to passthrough so
	// krew plugins and future kubectl verbs keep working. We do this before
	// handing argv to cobra to avoid "unknown command" errors. Passthrough
	// receives the full argv unchanged so kubectl-global flags (e.g. -v=8,
	// --server) survive.
	verb, hasVerb := firstVerb(args)
	if hasVerb && !isKnownCommand(root, verb) {
		code := runPassthrough(rootContext(), args)
		recordAudit(verb, args, code, time.Since(start))
		return code
	}

	root.SetArgs(args)
	err := root.ExecuteContext(rootContext())
	code := 0
	if err != nil {
		code = handleExecuteErr(err)
	}
	if hasVerb {
		recordAudit(verb, args, code, time.Since(start))
	}
	return code
}

// recordAudit captures one invocation in the audit log. Best-effort; failures
// are silenced so logging never breaks a user command.
func recordAudit(verb string, argv []string, exitCode int, dur time.Duration) {
	if Flags.Audit == "off" {
		return
	}
	user := ""
	if u := os.Getenv("USER"); u != "" {
		user = u
	} else if u := os.Getenv("LOGNAME"); u != "" {
		user = u
	}
	audit.MaybeRotate()
	audit.Record(audit.Event{
		User:       user,
		Cmd:        binaryName() + " " + verb,
		Argv:       append([]string{binaryName()}, argv...),
		Context:    Flags.Context,
		Namespace:  Flags.Namespace,
		Kubeconfig: Flags.Kubeconfig,
		Verb:       verb,
		AIProvider: usedAIProvider,
		DurationMS: dur.Milliseconds(),
		ExitCode:   exitCode,
	})
}

// usedAIProvider is set by AI-flavored commands (ai explain, diagnose, why,
// logs --ai) so the audit log can record which provider was contacted. Read
// only by recordAudit; lives at package scope so command RunEs can write to
// it without threading state through every signature.
var usedAIProvider string

func recordAIProvider(name string) { usedAIProvider = name }

// handleExecuteErr maps an error from cobra's Execute into an exit code,
// printing context-appropriate output along the way. The rules:
//
//   - *kubectl.ExitCodeError: kubectl already wrote its own error message to
//     stderr, so we just propagate the exit code silently.
//   - guardrail.ErrAborted: the user typed "no" (or mis-typed the confirmation
//     phrase); exit 130 to match Ctrl-C semantics, no extra message.
//   - errSilentFail: caller already printed; exit 1.
//   - anything else: print "error: <msg>" and exit 1.
func handleExecuteErr(err error) int {
	var ee *kubectl.ExitCodeError
	if errors.As(err, &ee) {
		return ee.Code
	}
	if errors.Is(err, guardrail.ErrAborted) {
		// User declined a prompt or mis-typed a confirmation — clean exit,
		// no scary "error:" line. 130 mirrors the SIGINT convention.
		return 130
	}
	if !errors.Is(err, errSilentFail) {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
	return 1
}

// NewRootCmd builds the root cobra command. Exported for tests.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           binaryName(),
		Short:         "A safer, prettier, AI-assisted wrapper around kubectl.",
		Long:          rootLongDescription,
		SilenceUsage:  true,
		SilenceErrors: true,
		// We don't set RunE on the root: invoking `sk` alone shows help.
	}

	f := root.PersistentFlags()
	f.StringVar(&Flags.Context, "context", "", "kubeconfig context to use")
	f.StringVarP(&Flags.Namespace, "namespace", "n", "", "namespace to use")
	f.StringVar(&Flags.Kubeconfig, "kubeconfig", "", "path to kubeconfig file")
	f.StringVar(&Flags.AIProvider, "ai", "", "AI provider: claude|gemini (auto-detect by default)")
	f.BoolVar(&Flags.Yes, "yes", false, "skip confirmation prompts (use with care)")
	f.StringVar(&Flags.DryRun, "dry-run", "auto", "dry-run mode: auto|server|client|none")
	f.BoolVar(&Flags.Plain, "plain", false, "disable color/TUI output")
	f.BoolVarP(&Flags.Verbose, "verbose", "v", false, "verbose output")
	f.StringVar(&Flags.Audit, "audit", "on", "audit logging: on|off")
	f.BoolVar(&Flags.NoContext, "no-context", false, "for AI: send only the literal prompt, no cluster data")

	root.PersistentPreRunE = persistentPreRunE
	root.PersistentPostRunE = persistentPostRunE

	// Subcommands. Each command file registers itself here.
	root.AddCommand(newVersionCmd())
	root.AddCommand(newCtxCmd())
	root.AddCommand(newNSCmd())
	root.AddCommand(newGetCmd())
	root.AddCommand(newDeleteCmd())
	root.AddCommand(newApplyCmd())
	root.AddCommand(newScaleCmd())
	root.AddCommand(newRolloutCmd())
	root.AddCommand(newDrainCmd())
	root.AddCommand(newCordonCmd())
	root.AddCommand(newAuditCmd())
	root.AddCommand(newAICmd())
	root.AddCommand(newDiagnoseCmd())
	root.AddCommand(newWhyCmd())
	root.AddCommand(newLogsCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newTUICmd())
	root.AddCommand(newUpgradeCmd())
	root.AddCommand(newPortForwardCmd())
	root.AddCommand(newWebCmd())

	// Force cobra to register its completion subcommand now (it's normally
	// lazy on first Execute()), so our passthrough routing sees it as a known
	// verb instead of forwarding to `kubectl completion`.
	root.InitDefaultCompletionCmd()

	return root
}

// rootContext returns a context.Context that is cancelled on SIGINT / SIGTERM.
// Long-running commands (e.g. `logs -f`, `port-forward`) honor it; the kubectl
// child process is also signalled directly by the runner.
func rootContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx
}

// persistentPreRunE runs before every subcommand. Configures shared state
// (UI styling, policy from config) and prints a context banner when the
// active context matches a policy-flagged pattern.
func persistentPreRunE(cmd *cobra.Command, args []string) error {
	ui.Init(Flags.Plain)
	loadPolicy()
	showContextBanner()
	_ = cmd
	_ = args
	return nil
}

// activePolicy is the per-invocation policy derived from config.yaml + the
// current kubectl context. Read by enhanced commands to decide whether an
// operation is forbidden or needs an upgraded confirmation. Set once per
// invocation in persistentPreRunE.
var activePolicy guardrail.Policy

// Policy returns the resolved policy for this invocation. Exported via this
// getter so command files can keep their imports short.
func Policy() guardrail.Policy { return activePolicy }

func loadPolicy() {
	cfg, err := config.Load()
	if err != nil {
		// Best-effort: if config is malformed, behave as if no policy was set.
		// We surface this only at -v in a future iteration.
		return
	}
	current := Flags.Context
	if current == "" {
		loader := kube.Loader{KubeconfigPath: Flags.Kubeconfig}
		current, _ = loader.CurrentContext()
	}
	activePolicy = guardrail.EffectivePolicy(cfg, current)
}

// activeBannerText caches whatever banner showContextBanner decided to print,
// so destructive commands can re-emit the same warning right before they ask
// for confirmation (the original banner can scroll off the screen during a
// long `kubectl diff`). Empty string means "nothing to show". activeBannerKind
// is a stable identifier ("info"|"warn"|"danger") so non-CLI surfaces can pick
// matching colors without inspecting the lipgloss style.
var (
	activeBannerText  string
	activeBannerStyle = ui.Banner
	activeBannerKind  = "info"
)

func showContextBanner() {
	activeBannerText = ""
	activeBannerStyle = ui.Banner
	activeBannerKind = "info"

	// Explicit policy wins: it's the user's deliberate choice and may override
	// the heuristic (e.g. they may want to label `prod-sandbox` as harmless).
	if activePolicy.MatchedPattern != "" {
		label := activePolicy.Banner
		if label == "" {
			label = "context matches " + activePolicy.MatchedPattern
		}
		activeBannerText = " ⚠ " + label + " "
		fmt.Fprintln(os.Stderr, ui.Render(activeBannerStyle, activeBannerText))
		return
	}

	// No explicit policy — fall back to the name-based classifier so users get
	// a warning the first time they switch into a prod-shaped context without
	// having configured anything.
	ctx := Flags.Context
	if ctx == "" {
		loader := kube.Loader{KubeconfigPath: Flags.Kubeconfig}
		ctx, _ = loader.CurrentContext()
	}
	level := guardrail.ClassifyContext(ctx)
	if level == guardrail.RiskNone {
		return
	}
	label := guardrail.AutoBannerLabel(level, ctx)
	activeBannerText = " ⚠ " + label + " "
	if level == guardrail.RiskCritical {
		activeBannerStyle = ui.Danger
		activeBannerKind = "danger"
	} else {
		activeBannerStyle = ui.Warning
		activeBannerKind = "warn"
	}
	fmt.Fprintln(os.Stderr, ui.Render(activeBannerStyle, activeBannerText))
}

// ReshowBanner reprints the most recently rendered context banner. Destructive
// commands call this just before the confirmation prompt so the warning is
// visible at the moment of decision, even if a long preview pushed it off
// screen.
func ReshowBanner() {
	if activeBannerText == "" {
		return
	}
	fmt.Fprintln(os.Stderr, ui.Render(activeBannerStyle, activeBannerText))
}

// ActiveBanner returns the most recently computed banner text and a stable
// style key ("info" | "warn" | "danger" | ""). Exported so non-CLI surfaces
// (e.g. the web UI) can render the same prod-context warning the terminal
// does. Returns empty text when no banner is active.
func ActiveBanner() (text, style string) {
	if activeBannerText == "" {
		return "", ""
	}
	return activeBannerText, activeBannerKind
}

// persistentPostRunE runs after a subcommand completes (success or error). It
// will eventually write the audit log entry. Stub for now.
func persistentPostRunE(cmd *cobra.Command, args []string) error {
	_ = cmd
	_ = args
	return nil
}

// errSilentFail signals that the error has already been reported and Execute
// should just propagate the exit code without printing again.
var errSilentFail = errors.New("silent failure")

// exitCodeFromErr maps an error to a process exit code. Commands that shell
// out to kubectl wrap the upstream exit code in *kubectl.ExitCodeError; others
// fall through to 1.
func exitCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	var ec *kubectl.ExitCodeError
	if errors.As(err, &ec) {
		return ec.Code
	}
	return 1
}

// firstVerb scans args left-to-right, skipping leading root flags, and returns
// the first positional token as the verb. We use it only for routing decisions
// (cobra vs passthrough). Passthrough always receives the full argv unchanged
// so kubectl-global flags survive.
//
// We classify only superkube's own root flags. Unknown flags are treated as
// boolean (consume one token) — this way kubectl-only flags don't accidentally
// "eat" the verb. The cost of being wrong: an unknown verb that follows an
// unknown value-taking flag may get treated as the verb. In practice this is
// fine because passthrough hands the full argv to kubectl, which will produce
// the same error the user would have seen from kubectl directly.
func firstVerb(args []string) (verb string, ok bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			// Everything after `--` is positional.
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		}
		if strings.HasPrefix(a, "-") {
			name := strings.TrimLeft(a, "-")
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name = name[:eq]
				i++
				continue
			}
			if rootFlagTakesValue(name) {
				i += 2
				continue
			}
			i++
			continue
		}
		return a, true
	}
	return "", false
}

// rootFlagTakesValue reports whether a superkube root flag of the given name
// (without leading dashes) consumes the next token as its value.
func rootFlagTakesValue(name string) bool {
	switch name {
	case "context", "namespace", "n", "kubeconfig", "ai", "dry-run", "audit":
		return true
	}
	return false
}

// isKnownCommand reports whether name is a direct subcommand of root. The
// cobra-builtin "help" command isn't in root.Commands() until the first
// Execute() so we list it explicitly; "completion" is added eagerly in
// NewRootCmd() via InitDefaultCompletionCmd.
func isKnownCommand(root *cobra.Command, name string) bool {
	if name == "help" {
		return true
	}
	for _, c := range root.Commands() {
		if c.Name() == name {
			return true
		}
		for _, alias := range c.Aliases {
			if alias == name {
				return true
			}
		}
	}
	return false
}

// binaryName returns the name the binary was invoked under, so help text
// reflects `sk` when called as `sk` and `superkube` otherwise.
func binaryName() string {
	if len(os.Args) == 0 {
		return "superkube"
	}
	name := os.Args[0]
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return "superkube"
	}
	return name
}

const rootLongDescription = `superkube wraps your existing kubectl with guardrails, prettier output, and
on-demand AI assistance.

Unknown verbs pass through to kubectl verbatim, so krew plugins, future kubectl
verbs, and your existing muscle memory all keep working. Run "` + "sk --help" + `"
to see the enhanced commands; everything else routes straight to kubectl.

Project: https://github.com/optimumsage/superkube  ` + "License: Apache-2.0"

// init wires version.String() into the cobra version flag so `sk --version`
// works in addition to the explicit `sk version` subcommand.
func init() {
	_ = version.String // referenced by newVersionCmd
}
