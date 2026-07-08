package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/ai"
	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/kubectl"
	"github.com/optimumsage/superkube/internal/tui"
	"github.com/optimumsage/superkube/internal/ui"
)

func newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Full-screen browser for pods / configmaps / secrets / ingresses",
		Long: `Live, full-screen resource browser backed by client-go informers.

Switch resource type with the number keys:

  1  Pods            (default)
  2  ConfigMaps
  3  Secrets         (data values masked in the YAML view)
  4  Ingresses

Then j/k or arrows to move, / to filter, enter to open the action menu, and:

  Y  yaml view       — kubectl get <kind> -o yaml (masked for secrets)
  e  edit            — opens 'sk <kind> edit' in your $EDITOR  (cm/sec/ing)
  X  delete          — typed-name confirm in-TUI, then 'sk delete <kind>'

Pods have extra actions: l (logs), d (describe), e (events), D (diagnose, AI),
y (why, AI), x (exec).

Honors -n / --namespace for the watched namespace (omit for all namespaces)
and --context for the kubectl context. Refuses to run without a TTY.`,
		RunE: runTUI,
	}
}

func runTUI(cmd *cobra.Command, args []string) error {
	_ = args
	if !ui.Interactive() {
		return errors.New("tui requires an interactive terminal (stdin + stdout TTY); did you redirect or pipe?")
	}
	loader := kube.Loader{KubeconfigPath: Flags.Kubeconfig, Context: Flags.Context}
	cs, err := loader.Clientset()
	if err != nil {
		return err
	}
	// os.Executable resolves the actual binary path so spawned subprocesses
	// (exec, delete) invoke the same superkube the user launched, even when
	// called via a symlink (the common `sk` case).
	bin, err := os.Executable()
	if err != nil {
		bin = os.Args[0]
	}
	currentCtx, _ := loader.CurrentContext()

	// kubectl runner is lazy-built but reused across producer calls.
	runner, runnerErr := kubectl.Default()

	opts := tui.Options{
		Clientset:  cs,
		Namespace:  Flags.Namespace, // empty = all namespaces
		Context:    currentCtx,
		BinaryPath: bin,
		ExtraArgs:  rootFlagsForSubprocess(),
	}
	if runnerErr == nil {
		opts.Describe = makeDescribeProducer(runner)
		opts.Events = makeEventsProducer(runner)
		opts.Diagnose = makeAIProducer(runner, "diagnose")
		opts.Why = makeAIProducer(runner, "why")
		opts.YAMLByKind = map[tui.Kind]tui.ProducerFn{
			tui.KindPod:       makeYAMLProducer(runner, "pod"),
			tui.KindConfigMap: makeYAMLProducer(runner, "configmap"),
			tui.KindSecret:    makeSecretYAMLProducer(runner),
			tui.KindIngress:   makeYAMLProducer(runner, "ingress"),
		}
	}
	return tui.Run(cmd.Context(), opts)
}

// rootFlagsForSubprocess captures the root flags that subprocess invocations
// (exec, delete) need to inherit, so an action against a pod runs against the
// same kubeconfig/context as the TUI itself.
func rootFlagsForSubprocess() []string {
	var out []string
	if Flags.Kubeconfig != "" {
		out = append(out, "--kubeconfig", Flags.Kubeconfig)
	}
	if Flags.Context != "" {
		out = append(out, "--context", Flags.Context)
	}
	return out
}

// makeDescribeProducer returns a producer that runs `kubectl describe pod <name>`
// and streams stdout into w.
func makeDescribeProducer(runner *kubectl.Runner) tui.ProducerFn {
	return func(ctx context.Context, ns, name string, w io.Writer) error {
		args := []string{"describe", "pod", name}
		if ns != "" {
			args = append(args, "-n", ns)
		}
		return runner.Run(ctx, args, kubectl.RunOpts{Stdout: w, Stderr: w})
	}
}

func makeYAMLProducer(runner *kubectl.Runner, kind string) tui.ProducerFn {
	return func(ctx context.Context, ns, name string, w io.Writer) error {
		args := []string{"get", kind, name, "-o", "yaml"}
		if ns != "" {
			args = append(args, "-n", ns)
		}
		return runner.Run(ctx, args, kubectl.RunOpts{Stdout: w, Stderr: w})
	}
}

// makeSecretYAMLProducer renders a secret's YAML with `data:` values masked,
// matching the default safety behavior of `sk secret view`. The TUI view is
// always read-only, so we don't expose a `--reveal` shortcut here — users
// who need decoded values run `sk secret view <name> --reveal` from a real
// shell where the audit + non-TTY guardrail applies.
func makeSecretYAMLProducer(runner *kubectl.Runner) tui.ProducerFn {
	return func(ctx context.Context, ns, name string, w io.Writer) error {
		args := []string{"get", "secret", name, "-o", "yaml"}
		if ns != "" {
			args = append(args, "-n", ns)
		}
		var buf bytes.Buffer
		if err := runner.Run(ctx, args, kubectl.RunOpts{Stdout: &buf, Stderr: w}); err != nil {
			return err
		}
		_, err := io.WriteString(w, maskSecretYAML(buf.String()))
		return err
	}
}

func makeEventsProducer(runner *kubectl.Runner) tui.ProducerFn {
	return func(ctx context.Context, ns, name string, w io.Writer) error {
		args := []string{
			"get", "events",
			"--field-selector", "involvedObject.name=" + name,
			"--sort-by=.lastTimestamp",
		}
		if ns != "" {
			args = append(args, "-n", ns)
		}
		return runner.Run(ctx, args, kubectl.RunOpts{Stdout: w, Stderr: w})
	}
}

// makeAIProducer wires the existing diagnose/why pipeline (gather inputs,
// render prompt, stream from the local AI provider) into the TUI's embedded
// text pane. Mirrors runAIDiagnostic but writes to w instead of os.Stdout
// and uses the explicit namespace from the selected pod.
func makeAIProducer(runner *kubectl.Runner, template string) tui.ProducerFn {
	return func(ctx context.Context, ns, name string, w io.Writer) error {
		provider, err := ai.Detect(Flags.AIProvider)
		if err != nil {
			fmt.Fprintf(w, "(ai unavailable: %v)\n", err)
			fmt.Fprintln(w, "Install `claude` or `agy` on PATH, or pin --ai=<name>.")
			return nil
		}
		recordAIProvider(provider.Name())

		fmt.Fprintf(w, "gathering describe / events / logs for pod %s/%s…\n", ns, name)
		resource := "pod/" + name
		inputs := gatherDiagnosticNS(ctx, runner, resource, "pod", name, ns, template == "diagnose")
		prompt, err := ai.Render(template, inputs)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "\nasking %s…\n\n", provider.Name())
		if err := provider.Run(ctx, prompt, w, ai.RunOpts{}); err != nil {
			return fmt.Errorf("%s: %w", provider.Name(), err)
		}
		return nil
	}
}
