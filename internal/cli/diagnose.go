package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/ai"
	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/kubectl"
)

func newDiagnoseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diagnose TYPE/NAME",
		Short: "Gather describe + events + logs and ask the AI provider what's wrong",
		Long: `Gathers diagnostic data for a pod (or other workload) and asks the local AI
provider to identify the most likely cause:

  - kubectl describe TYPE NAME
  - recent events with involvedObject = NAME
  - last 200 log lines from each container (redacted)

The combined payload is redacted before being sent to the provider, but
redaction is best-effort. Use --no-context if you don't want the data sent.`,
		Args: cobra.ExactArgs(1),
		RunE: runDiagnose,
	}
}

func runDiagnose(cmd *cobra.Command, args []string) error {
	// diagnose is open-ended, so it enriches with owner chain + siblings and can
	// opt into read-only tool access via --tools.
	return runAIDiagnostic(cmd, args[0], "diagnose", true, Flags.Tools)
}

// runAIDiagnostic is the shared body for `sk diagnose` and `sk why`: same data
// gathering, same provider invocation, different prompt template. The template
// name selects which set of instructions the model receives. enrichPod controls
// whether we fetch the owner chain + sibling pods (diagnose uses them; the why
// template does not). allowTools opts the run into read-only kubectl tool
// access (claude-enforced; antigravity best-effort).
func runAIDiagnostic(cmd *cobra.Command, resource, templateName string, enrichPod, allowTools bool) error {
	resourceType, resourceName := splitResource(resource)
	if resourceName == "" {
		return errors.New(templateName + ": expected TYPE/NAME (e.g. pod/foo)")
	}

	provider, err := ai.Detect(Flags.AIProvider)
	if err != nil {
		return err
	}
	recordAIProvider(provider.Name())

	runner, err := kubectl.Default()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), resolveAITimeout(allowTools))
	defer cancel()

	inputs := gatherDiagnostic(ctx, runner, resource, resourceType, resourceName, enrichPod)
	inputs.ToolsAllowed = allowTools

	prompt, err := ai.Render(templateName, inputs)
	if err != nil {
		return err
	}

	return streamAIResponse(ctx, provider, prompt, ai.RunOpts{AllowReadOnlyKubectl: allowTools})
}

// gatherDiagnostic shells out to kubectl to collect the describe/events/logs
// payload that AI prompts consume. Each step is best-effort: if logs aren't
// available yet (very fresh pod), we keep going with the rest. The current
// namespace from --namespace is honored; without it kubectl uses the context's
// default.
func gatherDiagnostic(ctx context.Context, runner *kubectl.Runner, resource, resourceType, resourceName string, enrichPod bool) ai.PromptInputs {
	return gatherDiagnosticNS(ctx, runner, resource, resourceType, resourceName, Flags.Namespace, enrichPod)
}

// gatherDiagnosticNS is the same as gatherDiagnostic but lets the caller pin
// the namespace explicitly. The TUI uses this so each action runs against the
// selected pod's actual namespace rather than --namespace.
//
// --no-context is honored strictly: when set, we send NO cluster data at all
// (no describe/events/logs, no owner chain, no siblings) — only the resource
// reference — matching the flag's documented "literal prompt only" promise.
func gatherDiagnosticNS(ctx context.Context, runner *kubectl.Runner, resource, resourceType, resourceName, namespace string, enrichPod bool) ai.PromptInputs {
	inputs := ai.PromptInputs{Resource: resource}
	if Flags.NoContext {
		return inputs
	}

	loader := kube.Loader{KubeconfigPath: Flags.Kubeconfig, Context: Flags.Context}
	if namespace != "" {
		inputs.Context, _, _ = loader.CurrentContextAndNamespace()
		inputs.Namespace = namespace
	} else {
		inputs.Context, inputs.Namespace, _ = loader.CurrentContextAndNamespace()
	}

	withNS := func(parts ...string) []string {
		if namespace == "" {
			return parts
		}
		return append(parts, "-n", namespace)
	}
	describeOut, _ := captureKubectl(ctx, runner, withNS("describe", resourceType, resourceName))
	eventsOut, _ := captureKubectl(ctx, runner, withNS(
		"get", "events", "--sort-by=.lastTimestamp",
		"--field-selector", "involvedObject.name="+resourceName,
	))
	logsOut, _ := captureKubectl(ctx, runner, withNS(
		"logs", resourceName, "--tail=200", "--all-containers=true", "--prefix=true",
	))
	inputs.Describe = describeOut
	inputs.Events = eventsOut
	inputs.Logs = ai.TruncateLogs(logsOut, 200)

	// Owner chain + sibling pods enrich the prompt for pod targets. Best-effort:
	// errors are swallowed because we'd rather render a partial diagnose than
	// fail on a missing RBAC permission. Skipped when enrichPod is false (e.g.
	// `sk why`, whose template renders neither) to avoid the API round-trips.
	if enrichPod && isPodKind(resourceType) {
		ns := inputs.Namespace
		if ns == "" {
			ns = "default"
		}
		inputs.OwnerChain, inputs.SiblingPods, _ = loader.EnrichPod(ctx, ns, resourceName)
	}
	return inputs
}

// isPodKind reports whether kind names a Pod resource in any of kubectl's
// accepted forms. The owner-chain enrichment is pod-specific because we walk
// pod.OwnerReferences upward; other workload kinds would need a different walk.
func isPodKind(kind string) bool {
	switch strings.ToLower(kind) {
	case "pod", "pods", "po":
		return true
	}
	return false
}

// splitResource accepts both "pod/foo" and "pod foo" forms. Returns
// type/name; if only a single token is supplied we default the type to "pod"
// because that's the by-far most common diagnose target.
func splitResource(s string) (kind, name string) {
	if idx := strings.IndexByte(s, '/'); idx >= 0 {
		return s[:idx], s[idx+1:]
	}
	return "pod", s
}

func captureKubectl(ctx context.Context, runner *kubectl.Runner, args []string) (string, error) {
	var stdout, stderr bytes.Buffer
	err := runner.Run(ctx, args, kubectl.RunOpts{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	out := stdout.String()
	if err != nil && stderr.Len() > 0 {
		// Surface the stderr inline so the AI can see e.g. "container has
		// previous logs available — try --previous". Don't fail the whole
		// diagnose just because logs aren't readable yet.
		out += "\n[stderr]\n" + stderr.String()
	}
	return out, err
}
