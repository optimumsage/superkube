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
	Tools      bool
	AITimeout  time.Duration
}

// Flags is the parsed root flag set. Populated by cobra before any RunE fires.
var Flags = &PersistentFlags{}

// Execute is the main entrypoint called from cmd/superkube/main.go. It returns
// the process exit code so main can pass it straight to os.Exit.
func Execute(args []string) int {
	root := NewRootCmd()
	start := time.Now()

	// Guarded commands set DisableFlagParsing (so unknown kubectl flags reach
	// kubectl), which means cobra never fills Flags.Context/Namespace/
	// Kubeconfig for them. Pre-parse those from raw argv — before routing —
	// so policy resolution, the banner and the audit row all see the context
	// the command will really run against. argv itself is left untouched.
	applyScannedFlags(scanGlobalFlags(args))
	loadPolicy()

	// If the first positional token is an unknown verb, route to passthrough so
	// krew plugins and future kubectl verbs keep working. We do this before
	// handing argv to cobra to avoid "unknown command" errors. Passthrough
	// receives the full argv unchanged so kubectl-global flags (e.g. -v=8,
	// --server) survive.
	verb, verbIdx, hasVerb := firstVerbAt(args)

	// Resource shortcut: `sk pods` → `sk get pods`. Only when the first verb
	// isn't already a known sk subcommand AND names a built-in kubectl resource.
	// Skipped when the verb came after a `--` separator (user explicitly asked
	// for raw kubectl semantics). Krew plugin names and unknown tokens stay on
	// the passthrough path below.
	if hasVerb && !isKnownCommand(root, verb) && looksLikeResource(verb) &&
		(verbIdx == 0 || args[verbIdx-1] != "--") {
		args = insertVerbAt(args, verbIdx, "get")
		verb = "get"
	}

	if hasVerb && !isKnownCommand(root, verb) {
		var code int
		if err := guardPassthrough(verb, args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			code = 1
		} else {
			code = runPassthrough(rootContext(), args)
		}
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
	if !auditEnabled() {
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
	f.StringVar(&Flags.AIProvider, "ai", "", "AI provider: claude|antigravity (auto-detect by default)")
	f.BoolVar(&Flags.Yes, "yes", false, "skip confirmation prompts (use with care)")
	f.StringVar(&Flags.DryRun, "dry-run", "auto", "dry-run mode: auto|server|client|none")
	f.BoolVar(&Flags.Plain, "plain", false, "disable color/TUI output")
	f.BoolVarP(&Flags.Verbose, "verbose", "v", false, "verbose output")
	f.StringVar(&Flags.Audit, "audit", "", "audit logging: on|off (default: on, or audit.enabled from config)")
	f.BoolVar(&Flags.NoContext, "no-context", false, "for AI: send only the literal prompt, no cluster data")
	f.BoolVar(&Flags.Tools, "tools", false, "for AI: let the provider run read-only kubectl to investigate (claude-enforced; antigravity best-effort)")
	f.DurationVar(&Flags.AITimeout, "ai-timeout", 0, "for AI: max time to wait for a response (default 90s, 5m with --tools)")

	root.PersistentPreRunE = persistentPreRunE

	// Subcommands. Each command file registers itself here.
	root.AddCommand(newVersionCmd())
	root.AddCommand(newCtxCmd())
	root.AddCommand(newNSCmd())
	root.AddCommand(newGetCmd())
	root.AddCommand(newDescribeCmd())
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
	root.AddCommand(newConfigmapCmd())
	root.AddCommand(newSecretCmd())
	root.AddCommand(newIngressCmd())

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
func persistentPreRunE(_ *cobra.Command, _ []string) error {
	ui.Init(Flags.Plain)
	// Execute normally loaded the policy already (before routing, so
	// passthrough is covered too); only load here when a caller built the
	// command tree directly, e.g. tests.
	if !policyLoaded {
		loadPolicy()
	}
	showContextBanner()
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

var (
	// policyLoaded records that loadPolicy ran for this invocation.
	policyLoaded bool
	// configErr is non-nil when config.yaml exists but is invalid. A broken
	// guardrail config must fail CLOSED: guarded commands refuse (see
	// requireValidConfig) instead of silently running with no policy.
	configErr error
	// activeCfg is the successfully loaded config (nil when absent/invalid).
	activeCfg *config.Config
)

func loadPolicy() {
	policyLoaded = true
	activePolicy = guardrail.Policy{}
	activeCfg = nil
	configErr = nil

	cfg, err := config.Load()
	if err != nil {
		configErr = err
		fmt.Fprintf(os.Stderr, "superkube: config %s is invalid: %v\n", config.ConfigFile(), err)
		return
	}
	activeCfg = cfg
	applyConfigDefaults(cfg)

	current := Flags.Context
	if current == "" {
		loader := kube.Loader{KubeconfigPath: Flags.Kubeconfig}
		current, _ = loader.CurrentContext()
	}
	activePolicy = guardrail.EffectivePolicy(cfg, current)
}

// applyConfigDefaults feeds config.yaml's ai.* settings in as defaults. An
// explicit --ai / --ai-timeout (parsed by cobra after this runs and so
// overriding it) and $SUPERKUBE_AI always win over the config file.
func applyConfigDefaults(cfg *config.Config) {
	if Flags.AIProvider == "" && strings.TrimSpace(os.Getenv("SUPERKUBE_AI")) == "" {
		Flags.AIProvider = cfg.AI.Provider
	}
	if Flags.AITimeout == 0 {
		Flags.AITimeout = cfg.AITimeout()
	}
}

// auditEnabled reports whether this invocation should be recorded.
// Precedence: --audit on|off, then audit.enabled in config, then on.
func auditEnabled() bool {
	switch strings.ToLower(Flags.Audit) {
	case "off":
		return false
	case "on":
		return true
	}
	if activeCfg != nil && activeCfg.Audit.Enabled != nil {
		return *activeCfg.Audit.Enabled
	}
	return true
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

// errSilentFail signals that the error has already been reported and Execute
// should just propagate the exit code without printing again.
var errSilentFail = errors.New("silent failure")

// firstVerb scans args left-to-right, skipping leading root flags, and returns
// the first positional token as the verb. We use it only for routing decisions
// (cobra vs passthrough) and the audit verb. Passthrough always receives the
// full argv unchanged so kubectl-global flags survive.
//
// Value-taking flags (superkube's own plus all of kubectl's global ones, see
// globalValueFlags) consume their value so it is never mistaken for the verb:
// `sk --as admin --context prod delete pod x` routes to delete, not "admin".
// Any other unknown flag is treated as boolean (consume one token) so that
// kubectl-only boolean flags don't eat the verb.
//
// `-v`/`--v` conflict: kubectl's is a verbosity level (`-v 8`, `--v=8`),
// superkube's `-v` is the boolean --verbose. We take a following numeric token
// as the value and otherwise treat it as boolean; verbs are never numeric.
func firstVerb(args []string) (verb string, ok bool) {
	v, _, found := firstVerbAt(args)
	return v, found
}

// firstVerbAt is firstVerb plus the verb's index in args (so callers that need
// to splice tokens — e.g. the `sk pods` → `sk get pods` rewrite — know where
// to insert). idx is meaningless when ok is false.
func firstVerbAt(args []string) (verb string, idx int, ok bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			// Everything after `--` is positional.
			if i+1 < len(args) {
				return args[i+1], i + 1, true
			}
			return "", 0, false
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			if flagConsumesNext(args, i, globalValueFlags) {
				i += 2
				continue
			}
			i++
			continue
		}
		return a, i, true
	}
	return "", 0, false
}

// scannedFlags are the root/global flags recovered from raw argv.
type scannedFlags struct {
	Context    string
	Kubeconfig string
	Namespace  string
	Audit      string
}

// scanGlobalFlags extracts --context, --kubeconfig, -n/--namespace and --audit
// from raw argv, in `--x=v` and `--x v` forms, anywhere (before or after the
// verb), last occurrence wins. Scanning stops at a bare `--` (what follows is
// a command for exec/debug, not our flags). Values of other value-taking
// flags are skipped so `--as -n` can't be misread. The argv is not modified.
func scanGlobalFlags(args []string) scannedFlags {
	var sf scannedFlags
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			continue
		}
		name, val, hasEq := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !hasEq && flagConsumesNext(args, i, globalValueFlags) {
			val = args[i+1]
			i++
		}
		switch name {
		case "context":
			sf.Context = val
		case "kubeconfig":
			sf.Kubeconfig = val
		case "namespace", "n":
			sf.Namespace = val
		case "audit":
			sf.Audit = val
		}
	}
	return sf
}

// applyScannedFlags copies scanned values into Flags without clobbering
// anything with an empty string.
func applyScannedFlags(sf scannedFlags) {
	if sf.Context != "" {
		Flags.Context = sf.Context
	}
	if sf.Kubeconfig != "" {
		Flags.Kubeconfig = sf.Kubeconfig
	}
	if sf.Namespace != "" {
		Flags.Namespace = sf.Namespace
	}
	if sf.Audit != "" {
		Flags.Audit = sf.Audit
	}
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
