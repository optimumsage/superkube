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

	"github.com/mattn/go-isatty"
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
	args = normalizeVerbosity(args)
	sf := scanGlobalFlags(args)
	applyScannedFlags(sf)
	// Policy is resolved over every context that COULD apply (fail-closed),
	// not just the scanner's best guess; see policyContexts.
	argvCands = scanCandidates(args, sf)
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
		showPassthroughBanner(isatty.IsTerminal(os.Stderr.Fd()))
		err := guardPassthrough(verb, args)
		if err == nil {
			err = backstopGuard(verb, verbIdx, args)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			code = 1
		} else {
			code = runPassthrough(rootContext(), args)
		}
		recordAudit(verb, args, code, time.Since(start))
		return code
	}

	// Put the verb first so cobra's own (flag-agnostic) subcommand search
	// agrees with firstVerbAt; leading flags simply follow the verb, which is
	// where DisableFlagParsing commands see them anyway.
	auditArgs := args
	if hasVerb && verbIdx > 0 && args[verbIdx-1] != "--" {
		reordered := append([]string{args[verbIdx]}, args[:verbIdx]...)
		args = append(reordered, args[verbIdx+1:]...)
	}
	root.SetArgs(args)
	err := root.ExecuteContext(rootContext())
	code := 0
	if err != nil {
		code = handleExecuteErr(err)
	}
	if hasVerb {
		recordAudit(verb, auditArgs, code, time.Since(start))
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
	// kubectl's --v=N verbosity. The long name "v" is distinct from the -v
	// shorthand of --verbose in pflag. Execute rewrites `-v 8`/`-v8` to
	// `--v=8` first, so the number is never read as a command; enhanced
	// commands (DisableFlagParsing) forward it to kubectl untouched.
	f.String("v", "", "kubectl log verbosity (forwarded to kubectl)")
	_ = f.MarkHidden("v")
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

	activePolicy = guardrail.EffectivePolicyFor(cfg, policyContexts(argvCands))
}

// argvCands is the raw-argv override candidate set for this invocation.
var argvCands argvCandidates

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
// Flag/value pairing comes from the single shared table in
// guardrail.FlagAt (superkube's flags, kubectl's globals and the per-verb
// value flags), so `sk --as admin --context prod -l app=x delete pod x`
// routes to delete, not "admin". Unknown flags are treated as boolean.
// --dry-run and --cascade only take the next token when it is a valid value
// (a bare `--dry-run` is legal), and `-v`/`--v` take it only when numeric:
// kubectl's is a verbosity level, superkube's `-v` the boolean --verbose.
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
		if f, isFlag := guardrail.FlagAt(args, i); isFlag {
			i++
			if f.UsesNext {
				i++
			}
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
	AI         string
	AITimeout  time.Duration
	Yes        bool
	Plain      bool
}

// scanGlobalFlags extracts the root flags from raw argv, in `--x=v` and
// `--x v` forms, anywhere (before or after the verb), last occurrence wins.
// Needed because DisableFlagParsing commands never let cobra fill Flags, and
// so that explicit flags beat config.yaml defaults for them too. Scanning
// stops at a bare `--`. `--ai` is only honored BEFORE the verb: after it,
// `logs --ai` is a boolean owned by that command. Note single-dash tokens are
// shorthand clusters, so `-context=prod` is not a context override. The argv
// is not modified. Best guess only: policy uses scanCandidates (fail-closed).
func scanGlobalFlags(args []string) scannedFlags {
	var sf scannedFlags
	_, verbIdx, hasVerb := firstVerbAt(args)
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		f, ok := guardrail.FlagAt(args, i)
		if !ok {
			continue
		}
		val := f.Val
		if f.UsesNext {
			val = args[i+1]
			i++
		}
		switch f.Name {
		case "context":
			sf.Context = val
		case "kubeconfig":
			sf.Kubeconfig = val
		case "namespace":
			sf.Namespace = val
		case "audit":
			sf.Audit = val
		case "ai":
			if hasVerb && i < verbIdx {
				sf.AI = val
			}
		case "ai-timeout":
			if d, err := time.ParseDuration(val); err == nil && d > 0 {
				sf.AITimeout = d
			}
		case "yes":
			sf.Yes = !f.HasVal || val == "true"
		case "plain":
			sf.Plain = !f.HasVal || val == "true"
		}
	}
	return sf
}

// argvCandidates lists every value that COULD be a context/kubeconfig
// override anywhere before a bare `--`, without trusting flag/value pairing
// (a `--context=dev` token may really be the value of `--field-manager`).
type argvCandidates struct {
	contexts, kubeconfigs               []string
	ambiguousContext, ambiguousKubeconf bool
}

func scanCandidates(args []string, sf scannedFlags) argvCandidates {
	var c argvCandidates
	raw := func(name string) (vals []string, empty bool) {
		for i, a := range args {
			if a == "--" {
				break
			}
			var v string
			switch {
			case strings.HasPrefix(a, "--"+name+"="):
				v = strings.TrimPrefix(a, "--"+name+"=")
			case a == "--"+name:
				if i+1 < len(args) {
					v = args[i+1]
				}
			default:
				continue
			}
			if v == "" {
				empty = true
			}
			vals = append(vals, v)
		}
		return vals, empty
	}
	ambiguous := func(vals []string, empty bool, walked string) bool {
		d := distinct(vals)
		return empty || len(d) > 1 || (len(d) == 1 && d[0] != walked)
	}
	cv, ce := raw("context")
	c.contexts = distinct(cv)
	c.ambiguousContext = ambiguous(cv, ce, sf.Context)
	kv, ke := raw("kubeconfig")
	c.kubeconfigs = distinct(kv)
	c.ambiguousKubeconf = ambiguous(kv, ke, sf.Kubeconfig)
	return c
}

func distinct(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// applyScannedFlags copies scanned values into Flags without clobbering
// anything with an empty value.
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
	if sf.AI != "" {
		Flags.AIProvider = sf.AI
	}
	if sf.AITimeout > 0 {
		Flags.AITimeout = sf.AITimeout
	}
	if sf.Yes {
		Flags.Yes = true
	}
	if sf.Plain {
		Flags.Plain = true
	}
}

// currentContextOf returns the kubeconfig's current-context ("" on error).
func currentContextOf(kubeconfig string) string {
	loader := kube.Loader{KubeconfigPath: kubeconfig}
	c, _ := loader.CurrentContext()
	return c
}

// policyContexts returns the candidate contexts policy must be evaluated
// against, best guess first. When the scan can't prove which context kubectl
// will use (several or unpaired --context tokens, or no override at all) the
// kubeconfig current-context(s) join the set, and policy is the union: a
// forbid in ANY candidate blocks.
func policyContexts(c argvCandidates) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	best := Flags.Context
	if best == "" {
		best = currentContextOf(Flags.Kubeconfig)
	}
	add(best)
	for _, s := range c.contexts {
		add(s)
	}
	if Flags.Context == "" || c.ambiguousContext {
		kcs := distinct(append(append([]string(nil), c.kubeconfigs...), Flags.Kubeconfig))
		if len(kcs) == 0 || c.ambiguousKubeconf || Flags.Kubeconfig == "" {
			kcs = append(kcs, "") // default kubeconfig resolution
		}
		for _, kc := range kcs {
			add(currentContextOf(kc))
		}
	}
	return out
}

// normalizeVerbosity rewrites kubectl's verbosity forms (`-v 8`, `-v8`,
// `-v=8`, `--v 8`) to `--v=8` so cobra never mistakes the number for a
// command. A bare `-v` stays superkube's boolean --verbose.
func normalizeVerbosity(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			return append(out, args[i:]...)
		}
		f, ok := guardrail.FlagAt(args, i)
		if !ok || f.Name != "v" || (!f.UsesNext && !f.HasVal) {
			out = append(out, args[i])
			continue
		}
		val := f.Val
		if f.UsesNext {
			val = args[i+1]
			i++
		}
		out = append(out, "--v="+val)
	}
	return out
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
