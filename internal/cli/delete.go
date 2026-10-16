package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/kubectl"
)

func newDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "delete TYPE NAME [NAME...]",
		Short:              "Delete resources with typed-name confirmation",
		Long:               deleteLongHelp,
		DisableFlagParsing: true,
		RunE:               runDelete,
	}
	return cmd
}

func runDelete(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	if len(args) == 0 {
		return errors.New("delete: TYPE NAME required, or --all")
	}

	yes := Flags.Yes || hasFlag(args, "--yes") || hasFlag(args, "-y")
	all := hasFlag(args, "--all")
	// Drop superkube-only flags before forwarding to kubectl.
	kubectlArgs := stripFlag(args, "--yes")
	kubectlArgs = stripFlag(kubectlArgs, "-y")

	// Policy check: forbidden operations are blocked outright, regardless of
	// --yes. Bypassing requires editing config.yaml.
	if err := guardOp("delete", args); err != nil {
		return err
	}

	// Decide confirmation style based on what's being deleted.
	ReshowBanner()
	positionals := positionalArgs(kubectlArgs)
	// Selector/manifest-driven deletes name no resource we could ask the user
	// to type, e.g. `delete -f x.yaml` or `delete pods -l app=x`.
	fromSource := hasFlag(kubectlArgs, "-f") || hasFlag(kubectlArgs, "--filename") ||
		hasFlag(kubectlArgs, "-k") || hasFlag(kubectlArgs, "--kustomize") ||
		hasFlag(kubectlArgs, "-l") || hasFlag(kubectlArgs, "--selector") ||
		hasFlag(kubectlArgs, "--field-selector")
	switch {
	case all:
		// `delete --all` is the highest-risk variant. Refuse without both --yes
		// and a typed "DELETE" phrase.
		if !yes {
			return errors.New("delete --all refused without --yes (and you'll still need to type DELETE)")
		}
		if err := guardrail.TypedPhrase(
			fmt.Sprintf("about to: kubectl %s", strings.Join(kubectlArgs, " ")),
			"DELETE",
			false, // ignore --yes here: --all always requires the typed phrase
		); err != nil {
			return err
		}
	case fromSource && len(positionals) < 2:
		if err := guardrail.TypedPhrase(
			fmt.Sprintf("about to: kubectl delete %s", strings.Join(kubectlArgs, " ")),
			"DELETE",
			yes,
		); err != nil {
			return err
		}
	default:
		// positionals look like: [TYPE NAME [NAME...]] or [TYPE/NAME ...].
		resourceType, names := splitDeleteTargets(positionals)
		if len(names) == 0 {
			return errors.New("delete: need TYPE and at least one NAME (or --all)")
		}
		switch len(names) {
		case 1:
			if err := guardrail.TypedName(
				strings.TrimSpace(fmt.Sprintf("delete %s %s", resourceType, names[0])),
				names[0],
				yes,
			); err != nil {
				return err
			}
		default:
			phrase := fmt.Sprintf("DELETE-%d", len(names))
			if err := guardrail.TypedPhrase(
				strings.TrimSpace(fmt.Sprintf("delete %d %s: %s", len(names), resourceType, strings.Join(names, ", "))),
				phrase,
				yes,
			); err != nil {
				return err
			}
		}
	}

	// Run the real delete.
	runner, err := kubectl.New()
	if err != nil {
		return err
	}
	return runner.Run(cmd.Context(), append([]string{"delete"}, kubectlArgs...), kubectl.RunOpts{})
}

// splitDeleteTargets turns delete's positionals into (type, names). Supports
// `TYPE NAME...` and the all-`TYPE/NAME` form (type is then empty and each
// name keeps its kind prefix, which is what the user types to confirm).
func splitDeleteTargets(positionals []string) (resourceType string, names []string) {
	if len(positionals) == 0 {
		return "", nil
	}
	allSlashed := true
	for _, p := range positionals {
		if !strings.Contains(p, "/") {
			allSlashed = false
			break
		}
	}
	if allSlashed {
		return "", positionals
	}
	return positionals[0], positionals[1:]
}

const deleteLongHelp = `Delete resources with typed-name confirmation.

Single resource: you must type the resource name verbatim.
Multiple names:  you must type the phrase "DELETE-N" where N is the count.
-f / -k / -l:    deleting from a manifest or selector: type the phrase "DELETE".
--all:           refuses without --yes AND a typed "DELETE" phrase.

Flags after the verb are passed to kubectl verbatim, so the usual kubectl
options (--grace-period, --force, --selector, etc.) all work. Put --yes before
the verb (sk --yes delete ...) to bypass the prompt; using it on a single
resource skips the typed prompt, but --all still requires the typed phrase.`
