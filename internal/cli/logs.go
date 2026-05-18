package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/ai"
	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/kubectl"
	"github.com/optimumsage/superkube/internal/ui"
)

func newLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "logs [-f] [--tail=N] [--ai] POD",
		Short:              "View logs (passthrough); `--ai` summarizes errors via the AI provider",
		Long:               logsLongHelp,
		DisableFlagParsing: true,
		RunE:               runLogs,
	}
}

func runLogs(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	// --multi=<target> branch: stream logs from several pods at once.
	if multi, ok := flagValue(args, "--multi"); ok {
		return runLogsMulti(cmd, args, multi)
	}

	if len(args) == 0 {
		return errors.New("logs: POD name required")
	}

	useAI := hasFlag(args, "--ai")
	if !useAI {
		runner, err := kubectl.Default()
		if err != nil {
			return err
		}
		// On TTY, stream through a line-by-line colorizer so ERROR/WARN/INFO
		// tokens, panics, stack frames, HTTP status codes, and timestamps are
		// highlighted as kubectl produces them (incl. `-f` follow mode). On
		// pipes/redirects we fall through to the default RunOpts so output is
		// byte-identical to `kubectl logs` for jq/awk/grep callers.
		if ui.Styled() {
			lc := newLineColorizer(os.Stdout)
			defer func() { _ = lc.Flush() }()
			return runner.Run(cmd.Context(), append([]string{"logs"}, args...), kubectl.RunOpts{
				Stdout: lc,
				Stderr: os.Stderr,
			})
		}
		return runner.Run(cmd.Context(), append([]string{"logs"}, args...), kubectl.RunOpts{})
	}

	// --ai mode: incompatible with follow/streaming.
	if hasFlag(args, "-f") || hasFlag(args, "--follow") {
		return errors.New("logs --ai cannot be combined with -f / --follow (analysis runs on a finite buffer)")
	}

	kubectlArgs := stripFlag(args, "--ai")
	// Default to a reasonable tail when the user didn't pick one — full pod
	// history can be huge and the model has a context limit.
	if !hasFlag(kubectlArgs, "--tail") {
		kubectlArgs = append(kubectlArgs, "--tail=200")
	}

	runner, err := kubectl.Default()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	err = runner.Run(ctx, append([]string{"logs"}, kubectlArgs...), kubectl.RunOpts{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	logs := stdout.String()
	if stderr.Len() > 0 {
		logs += "\n[kubectl stderr]\n" + stderr.String()
	}
	if err != nil && logs == "" {
		return err
	}

	provider, err := ai.Detect(Flags.AIProvider)
	if err != nil {
		return err
	}
	recordAIProvider(provider.Name())

	inputs := ai.PromptInputs{
		Resource: positionalArgs(args)[0],
		Logs:     ai.TruncateLogs(logs, 200),
	}
	if !Flags.NoContext {
		loader := kube.Loader{KubeconfigPath: Flags.Kubeconfig, Context: Flags.Context}
		inputs.Context, _ = loader.CurrentContext()
		inputs.Namespace, _ = loader.CurrentNamespace()
	}

	prompt, err := ai.Render("logs", inputs)
	if err != nil {
		return err
	}

	w, stop := ui.SpinUntilFirstByte("asking "+provider.Name()+"…", os.Stdout)
	defer stop()
	if err := provider.Run(ctx, prompt, w, ai.RunOpts{}); err != nil {
		return fmt.Errorf("%s: %w", provider.Name(), err)
	}
	fmt.Fprintln(os.Stdout)
	return nil
}

// runLogsMulti drives a multi-pod tail via client-go. target is either a
// workload reference ("deploy/web") or a raw label selector ("app=web").
func runLogsMulti(cmd *cobra.Command, args []string, target string) error {
	follow := hasFlag(args, "-f") || hasFlag(args, "--follow")
	container := ""
	if v, ok := flagValue(args, "-c"); ok {
		container = v
	}
	if v, ok := flagValue(args, "--container"); ok {
		container = v
	}
	var tail int64 = 200
	if v, ok := flagValue(args, "--tail"); ok {
		// Pflag rejects on its own elsewhere; here we accept any int-ish input
		// and fall back to the default on parse failure to keep the UX kind.
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 0 {
			tail = n
		}
	}

	opts := kube.MultiLogOpts{
		Namespace: effectiveNamespace(args),
		Container: container,
		TailLines: tail,
		Follow:    follow,
	}
	// Distinguish workload-shorthand from raw selector by the presence of "/".
	if strings.Contains(target, "/") && !strings.Contains(target, "=") {
		opts.Workload = target
	} else {
		opts.Selector = target
	}

	loader := kube.Loader{KubeconfigPath: Flags.Kubeconfig, Context: effectiveContext(args)}
	prefixer := podPrefixer()
	// On TTY, also colorize log severity tokens inside each line. podPrefixer
	// already paints the `[pod] ` prefix in its per-pod color; the colorizer
	// only touches recognized severity tokens / HTTP codes / timestamps later
	// in the line, so the two layers compose cleanly.
	var out io.Writer = os.Stdout
	if ui.Styled() {
		lc := newLineColorizer(os.Stdout)
		defer lc.Flush()
		out = lc
	}
	return loader.TailMulti(cmd.Context(), opts, out, prefixer)
}

// podPrefixer returns a LinePrefixer that colorizes the [pod-name] tag with a
// stable per-pod color, so each pod's lines are visually grouped. Colors come
// from a small ANSI 256 palette; pod name → palette index is hash-based, so
// the same pod gets the same color across invocations.
func podPrefixer() kube.LinePrefixer {
	if !ui.Styled() {
		return func(name string, _ int) string { return "[" + name + "] " }
	}
	palette := []lipgloss.Color{"39", "208", "141", "10", "201", "33", "11", "13", "45", "166"}
	cache := map[string]lipgloss.Style{}
	return func(name string, _ int) string {
		st, ok := cache[name]
		if !ok {
			h := fnv.New32a()
			_, _ = h.Write([]byte(name))
			color := palette[int(h.Sum32())%len(palette)]
			st = lipgloss.NewStyle().Foreground(color)
			cache[name] = st
		}
		return st.Render("["+name+"] ") + ""
	}
}

const logsLongHelp = `View logs.

Modes:
  default        Plain kubectl logs (shells out, --previous / -f / --tail
                 all behave identically because we pass through verbatim).
  --ai           Capture the tail, redact obvious secret-shaped strings, and
                 ask the local AI provider for a summary of errors with
                 likely root cause. Cannot combine with -f.
  --multi=TARGET Stream logs from many pods at once, prefixed and colored
                 per pod. TARGET is either a workload (deploy/web,
                 statefulset/db, daemonset/log, replicaset/x) or a raw
                 label selector (app=web,tier=frontend). Supports -f.

Examples:
  sk logs my-pod                       # plain
  sk logs my-pod -f                    # streamed
  sk logs my-pod --ai                  # AI summary of the last 200 lines
  sk logs --multi=deploy/web -f        # tail every web pod, colored prefix
  sk logs --multi=app=db --tail=50     # last 50 lines from every db pod`
