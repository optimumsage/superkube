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
	// origArgs is what the user typed. Passthrough and the audit row always
	// get it; any normalization below is only for cobra's view.
	origArgs := args

	// Guarded commands set DisableFlagParsing (so unknown kubectl flags reach
	// kubectl), which means cobra never fills Flags for them. Pre-parse the
	// root flags from raw argv — before routing — so policy resolution, the
	// banner and the audit row all see the context the command will really
	// run against. Policy is resolved over every context that COULD apply
	// (fail-closed), not just the scanner's best guess; see policyContexts.
	sf := scanGlobalFlags(args)
	if sf.Err != nil {
		fmt.Fprintln(os.Stderr, "error:", sf.Err)
		return 1
	}
	applyScannedFlags(sf)
	argvCands = scanCandidates(args, sf)
	loadPolicy()

	// If the first positional token is an unknown verb, route to passthrough so
	// krew plugins and future kubectl verbs keep working. We do this before
	// handing argv to cobra to avoid "unknown command" errors. Passthrough
	// receives the full argv unchanged so kubectl-global flags (e.g. -v=8,
	// --server) survive.
	verb, verbIdx, hasVerb := firstVerbAt(args)

	// Fail closed on command-specific flags placed before the verb: we can't
	// be sure which token is the verb, so rather than risk routing a guarded
	// command around its guardrails we ask the user to reorder.
	if err := leadAmbiguity(root, args, verbIdx, hasVerb); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		recordAudit(verb, origArgs, 1, time.Since(start))
		return 1
	}

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
		if err := guardPassthrough(verb, args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			code = 1
		} else {
			code = runPassthrough(rootContext(), args)
		}
		recordAudit(verb, origArgs, code, time.Since(start))
		return code
	}

	root.SetArgs(cobraArgs(root, args, verbIdx, hasVerb))
	err := root.ExecuteContext(rootContext())
	code := 0
	if err != nil {
		code = handleExecuteErr(err)
	}
	if hasVerb {
		recordAudit(verb, origArgs, code, time.Since(start))
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
	f.StringVar(&Flags.DryRun, "dry-run", "auto", "dry-run mode: auto|server|client|none (bare --dry-run means client; values need =)")
	f.Lookup("dry-run").NoOptDefVal = "client" // kubectl semantics: never eats the next token
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
// original argv so kubectl-global flags survive.
//
// Flag/value pairing and short-cluster parsing come from the single shared
// table in guardrail.FlagsAt (superkube's flags, kubectl's globals and the
// per-verb value flags), so `sk --as admin --context prod delete pod x`
// routes to delete, not "admin". Unknown flags are treated as boolean.
// `--dry-run`/`--cascade` never take the next token (pflag requires `=`).
// Before the verb, `--ai` is superkube's provider flag and takes a value; after
// it, `logs --ai` is a boolean.
func firstVerb(args []string) (verb string, ok bool) {
	v, _, found := firstVerbAt(args)
	return v, found
}

// parseTok parses the flag token at args[i]. lead is true for tokens before
// the verb, where `--ai` takes a value. n is the number of argv tokens the
// flag occupies.
func parseTok(args []string, i int, lead bool) (fs []guardrail.Flag, n int, ok bool) {
	fs, ok = guardrail.FlagsAt(args, i)
	if !ok {
		return nil, 0, false
	}
	last := &fs[len(fs)-1]
	if lead && last.Name == "ai" && !last.HasVal && i+1 < len(args) {
		last.UsesNext = true
	}
	n = 1
	if last.UsesNext {
		n = 2
	}
	return fs, n, true
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
		if _, n, isFlag := parseTok(args, i, true); isFlag {
			i += n
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
	DryRun     string
	AITimeout  time.Duration
	Yes        bool
	Plain      bool
	Verbose    bool
	NoContext  bool
	Tools      bool
	// Err is the first invalid flag value found; Execute refuses to run
	// anything when it is set.
	Err error
}

func (sf *scannedFlags) fail(err error) {
	if sf.Err == nil {
		sf.Err = err
	}
}

func truthy(f guardrail.Flag, val string) bool {
	if !f.HasVal {
		return true
	}
	switch strings.ToLower(val) {
	case "false", "0", "f", "no":
		return false
	}
	return true
}

// scanGlobalFlags extracts the root flags from raw argv, in `--x=v` and
// `--x v` forms, anywhere (before or after the verb), last occurrence wins.
// Needed because DisableFlagParsing commands never let cobra fill Flags, and
// so explicit flags beat config.yaml defaults for them too. Scanning stops at
// a bare `--`. `--ai` is only honored BEFORE the verb: after it, `logs --ai`
// is a boolean owned by that command. Boolean flags honor `=false`. Single-dash
// tokens are shorthand clusters, so `-context=prod` is not a context override.
// The argv is not modified. Best guess only: policy uses scanCandidates.
func scanGlobalFlags(args []string) scannedFlags {
	var sf scannedFlags
	_, verbIdx, hasVerb := firstVerbAt(args)
	for i := 0; i < len(args); {
		if args[i] == "--" {
			break
		}
		lead := hasVerb && i < verbIdx
		fs, n, ok := parseTok(args, i, lead)
		if !ok {
			i++
			continue
		}
		for j, f := range fs {
			val := f.Val
			if j == len(fs)-1 && f.UsesNext {
				val = args[i+1]
			}
			switch f.Name {
			case "context":
				sf.Context = val
			case "kubeconfig":
				sf.Kubeconfig = val
			case "namespace":
				sf.Namespace = val
			case "audit":
				// Values are validated: a value-less `--audit --yes` must not
				// silently swallow the confirmation flag.
				if val != "on" && val != "off" {
					sf.fail(fmt.Errorf("invalid --audit value %q (want on|off)", val))
				}
				sf.Audit = val
			case "ai":
				if lead {
					switch strings.ToLower(val) {
					case "claude", "antigravity", "agy":
					default:
						sf.fail(fmt.Errorf("invalid --ai provider %q (want claude|antigravity)", val))
					}
					sf.AI = val
				}
			case "ai-timeout":
				d, err := time.ParseDuration(val)
				if err != nil || d <= 0 {
					sf.fail(fmt.Errorf("invalid --ai-timeout value %q (want a duration such as 90s)", val))
				}
				sf.AITimeout = d
			case "dry-run":
				sf.DryRun = "client"
				if f.HasVal {
					sf.DryRun = val
				}
			case "yes":
				sf.Yes = truthy(f, val)
			case "plain":
				sf.Plain = truthy(f, val)
			case "verbose":
				sf.Verbose = truthy(f, val)
			case "no-context":
				sf.NoContext = truthy(f, val)
			case "tools":
				sf.Tools = truthy(f, val)
			}
		}
		i += n
	}
	return sf
}

// kubectlVerbs are kubectl's own verbs; together with sk's subcommands they
// are the tokens that make an ambiguous command line dangerous.
var kubectlVerbs = map[string]bool{
	"get": true, "describe": true, "create": true, "delete": true, "apply": true,
	"edit": true, "patch": true, "replace": true, "label": true, "annotate": true,
	"taint": true, "scale": true, "rollout": true, "drain": true, "cordon": true,
	"uncordon": true, "exec": true, "debug": true, "cp": true, "run": true,
	"expose": true, "set": true, "autoscale": true, "attach": true, "logs": true,
	"port-forward": true, "proxy": true, "top": true, "cluster-info": true,
	"config": true, "auth": true, "certificate": true, "wait": true, "diff": true,
	"kustomize": true, "plugin": true, "events": true, "api-resources": true,
	"api-versions": true, "explain": true, "version": true, "completion": true,
}

// leadAmbiguity fails closed on leading flags we can't account for. We never
// silently drop or guess:
//
//   - A leading --help/-h/--version in front of a superkube command is refused
//     (it must follow the verb, e.g. `sk delete --help`), so it can never be
//     swallowed while the command still runs.
//   - If a leading flag is neither a superkube root flag nor a known kubectl
//     global (`-l app=web delete`, `-Rf x.yaml delete`, `--unknown foo delete`)
//     we cannot know whether it takes a value, hence which token is the verb;
//     if ANY later positional is a kubectl or superkube verb, guessing wrong
//     could route a mutation around its guardrails, so we refuse.
//
// When routing is unambiguous (only known flags before the verb) nothing is
// refused: `sk auth can-i delete pods` is unaffected.
func leadAmbiguity(root *cobra.Command, args []string, verbIdx int, hasVerb bool) error {
	if !hasVerb {
		return nil
	}
	ambiguous, helpOrVersion := false, false
	for i := 0; i < verbIdx; {
		fs, n, ok := parseTok(args, i, true)
		if !ok {
			i++
			continue
		}
		for _, f := range fs {
			if f.Name == "help" || f.Name == "version" {
				helpOrVersion = true
			}
			if !guardrail.IsSkRoot(f.Name) && !guardrail.IsKubectlGlobal(f.Name) {
				ambiguous = true
			}
		}
		i += n
	}
	verb := args[verbIdx]
	if helpOrVersion && isKnownCommand(root, verb) {
		return fmt.Errorf("put --help/--version after the verb (e.g. sk %s --help); a leading one is not supported", verb)
	}
	if !ambiguous {
		return nil
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		if kubectlVerbs[a] || isKnownCommand(root, a) {
			return fmt.Errorf("put command flags after the verb (e.g. sk %s -l app=web …): the flags before the verb are ambiguous", a)
		}
	}
	return nil
}


// childByName finds a direct subcommand by name or alias.
func childByName(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name {
			return c
		}
		for _, a := range c.Aliases {
			if a == name {
				return c
			}
		}
	}
	return nil
}

// cobraArgs builds the argv cobra sees. Cobra's own subcommand search is not
// flag-aware (it assumes any unknown `--flag` takes a value), so we put the
// verb (and subcommand chain, e.g. `configmap edit`) first. Invariant: a
// leading flag is never silently dropped. Superkube root flags that are fully
// applied through Flags by scanGlobalFlags (--yes, --plain, --audit,
// --ai, --ai-timeout, --no-context, --tools, --verbose) are consumed here
// (keeping `--ai claude` in argv would leak `claude` into kubectl args);
// EVERYTHING else (kubectl globals, --dry-run, --help, ...) is moved to just
// after the command chain, ahead of the leaf's own args, where DisableFlagParsing commands
// forward it to kubectl and honor it. Flags interleaved with the subcommand
// chain (`configmap -n kube-system edit x`) are moved the same way, so the
// leaf and its policy check always see them. kubectl's `-v N` forms are
// normalized to `--v=N`.
func cobraArgs(root *cobra.Command, args []string, verbIdx int, hasVerb bool) []string {
	if !hasVerb || verbIdx == 0 || args[verbIdx-1] == "--" {
		return normalizeVerbosity(args, verbIdx, hasVerb)
	}
	var moved []string
	for i := 0; i < verbIdx; {
		fs, n, ok := parseTok(args, i, true)
		if !ok {
			return normalizeVerbosity(args, verbIdx, hasVerb) // stray `--`: leave as typed
		}
		consumed := true
		for _, f := range fs {
			if !guardrail.IsSkRoot(f.Name) || f.Name == "dry-run" || f.Name == "help" || f.Name == "version" {
				consumed = false
			}
		}
		if !consumed {
			moved = append(moved, args[i:i+n]...)
		}
		i += n
	}
	cmd := childByName(root, args[verbIdx])
	chain := []string{args[verbIdx]}
	var rest []string
	for j := verbIdx + 1; j < len(args); {
		if args[j] == "--" {
			rest = append(rest, args[j:]...)
			break
		}
		if _, n, isFlag := parseTok(args, j, false); isFlag {
			moved = append(moved, args[j:j+n]...)
			j += n
			continue
		}
		if cmd != nil && len(rest) == 0 {
			if sub := childByName(cmd, args[j]); sub != nil {
				chain = append(chain, args[j])
				cmd = sub
				j++
				continue
			}
		}
		rest = append(rest, args[j])
		j++
		// After the first non-subcommand token the rest is the leaf's own
		// argv; copy it verbatim (flags included) so pairing stays intact.
		rest = append(rest, args[j:]...)
		break
	}
	// Moved flags go right after the chain, ahead of the leaf's own argv, so
	// every flag keeps its original order relative to everything after it
	// (pflag's last-occurrence-wins must resolve exactly as the user typed).
	out := append([]string(nil), chain...)
	out = append(out, moved...)
	out = append(out, rest...)
	return normalizeVerbosity(out, 0, false)
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
	// raw returns every `--name=V` / `--name V` value and whether any of them
	// is ambiguous: empty, or preceded by a flag that either consumes it as
	// its own value or is not a KNOWN boolean (so it might have).
	raw := func(name string) (vals []string, ambiguous bool) {
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
				ambiguous = true
			}
			if i > 0 && strings.HasPrefix(args[i-1], "-") && args[i-1] != "--" && !strings.Contains(args[i-1], "=") {
				if fs, ok := guardrail.FlagsAt(args, i-1); ok {
					last := fs[len(fs)-1]
					if last.UsesNext || (!last.HasVal && !guardrail.IsKnownBool(last.Name)) {
						ambiguous = true
					}
				}
			}
			vals = append(vals, v)
		}
		return vals, ambiguous
	}
	disagree := func(vals []string, walked string) bool {
		d := distinct(vals)
		return len(d) > 1 || (len(d) == 1 && d[0] != walked)
	}
	cv, ca := raw("context")
	c.contexts = distinct(cv)
	c.ambiguousContext = ca || disagree(cv, sf.Context)
	kv, ka := raw("kubeconfig")
	c.kubeconfigs = distinct(kv)
	c.ambiguousKubeconf = ka || disagree(kv, sf.Kubeconfig)
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
	if sf.DryRun != "" {
		Flags.DryRun = sf.DryRun
	}
	if sf.AITimeout > 0 {
		Flags.AITimeout = sf.AITimeout
	}
	Flags.Yes = Flags.Yes || sf.Yes
	Flags.Plain = Flags.Plain || sf.Plain
	Flags.Verbose = Flags.Verbose || sf.Verbose
	Flags.NoContext = Flags.NoContext || sf.NoContext
	Flags.Tools = Flags.Tools || sf.Tools
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
// command. It walks argv with real flag/value pairing, so a value that merely
// looks like a verbosity flag (`--token -v8`) is left alone. A bare `-v`
// stays superkube's boolean --verbose. Only cobra's view is normalized;
// passthrough and audit keep the original argv.
func normalizeVerbosity(args []string, verbIdx int, hasVerb bool) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); {
		if args[i] == "--" {
			return append(out, args[i:]...)
		}
		// `--ai` takes a value only before the verb, same as the scanner.
		fs, n, ok := parseTok(args, i, hasVerb && i < verbIdx)
		if ok && len(fs) == 1 && fs[0].Name == "v" && (fs[0].UsesNext || fs[0].HasVal) {
			val := fs[0].Val
			if fs[0].UsesNext {
				val = args[i+1]
			}
			out = append(out, "--v="+val)
		} else if ok {
			out = append(out, args[i:i+n]...)
		} else {
			out = append(out, args[i])
			n = 1
		}
		i += n
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
