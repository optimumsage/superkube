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
	if rule, blocked := Policy().IsForbidden("delete", args); blocked {
		return fmt.Errorf("delete refused by policy %q for context matching %q",
			rule, Policy().MatchedPattern)
	}

	// Decide confirmation style based on what's being deleted.
	ReshowBanner()
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
	default:
		positionals := positionalArgs(args)
		// positionals look like: [TYPE NAME [NAME...]]
		if len(positionals) < 2 {
			return errors.New("delete: need TYPE and at least one NAME (or --all)")
		}
		resourceType := positionals[0]
		names := positionals[1:]
		switch len(names) {
		case 1:
			if err := guardrail.TypedName(
				fmt.Sprintf("delete %s %s", resourceType, names[0]),
				names[0],
				yes,
			); err != nil {
				return err
			}
		default:
			phrase := fmt.Sprintf("DELETE-%d", len(names))
			if err := guardrail.TypedPhrase(
				fmt.Sprintf("delete %d %s: %s", len(names), resourceType, strings.Join(names, ", ")),
				phrase,
				yes,
			); err != nil {
				return err
			}
		}
	}

	// Run the real delete.
	runner, err := kubectl.Default()
	if err != nil {
		return err
	}
	return runner.Run(cmd.Context(), append([]string{"delete"}, kubectlArgs...), kubectl.RunOpts{})
}

const deleteLongHelp = `Delete resources with typed-name confirmation.

Single resource: you must type the resource name verbatim.
Multiple names:  you must type the phrase "DELETE-N" where N is the count.
--all:           refuses without --yes AND a typed "DELETE" phrase.

Flags after the verb are passed to kubectl verbatim, so the usual kubectl
options (--grace-period, --force, --selector, etc.) all work. Put --yes before
the verb (sk --yes delete ...) to bypass the prompt; using it on a single
resource skips the typed prompt, but --all still requires the typed phrase.`
