package cli

import (
	"fmt"

	"github.com/optimumsage/superkube/internal/config"
	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/ui"
)

// promptYesNo is guardrail.YesNo behind a variable so tests can simulate a user
// declining the prompt (a real prompt needs a TTY).
var promptYesNo = guardrail.YesNo

// requireValidConfig makes guarded operations fail closed when config.yaml is
// present but invalid: with the policy unknown we cannot tell whether the
// operation is forbidden, so we refuse rather than run unprotected.
func requireValidConfig(op string) error {
	if configErr == nil {
		return nil
	}
	return fmt.Errorf("%s refused: config %s is invalid (%v); fix or move the file", op, config.ConfigFile(), configErr)
}

// guardOp is the common pre-flight for every mutating command: the config
// must be valid and the per-context policy must not forbid verb+args. Policy
// beats --yes; only editing config.yaml unblocks a forbidden operation.
func guardOp(verb string, args []string) error {
	if err := requireValidConfig(verb); err != nil {
		return err
	}
	if rule, blocked := Policy().IsForbidden(verb, args); blocked {
		return fmt.Errorf("%s refused by policy %q for context matching %q",
			verb, rule, Policy().MatchedPattern)
	}
	return nil
}

// confirmYesNo asks a yes/no question, upgraded to a typed phrase when the
// effective policy has require_typed_confirm set. A declined prompt returns
// guardrail.ErrAborted (exit 130), never nil.
func confirmYesNo(prompt, detail, phrase string, yes bool) error {
	if Policy().UpgradeStyle("yesno") == "typed-phrase" {
		return guardrail.TypedPhrase(prompt+" "+detail, phrase, yes)
	}
	return promptYesNo(prompt, detail, yes)
}

// readOnlyVerbs are kubectl verbs that never mutate cluster state whatever
// their subcommand. They keep working when config.yaml is broken (with the
// warning).
var readOnlyVerbs = map[string]bool{
	"get": true, "describe": true, "logs": true, "top": true, "explain": true,
	"api-resources": true, "api-versions": true, "version": true,
	"cluster-info": true, "completion": true, "events": true, "options": true,
	"help": true, "diff": true, "wait": true, "kustomize": true,
}

// readOnlySubs lists, for verbs that mix reads and writes, the subcommands
// that are read-only. Anything not listed (including unknown subcommands and
// krew plugins) is treated as mutating and fails closed.
var readOnlySubs = map[string]map[string]bool{
	"auth":    {"can-i": true, "whoami": true},
	"config":  {"view": true, "get-contexts": true, "get-clusters": true, "get-users": true, "current-context": true},
	"rollout": {"status": true, "history": true},
	"plugin":  {"list": true},
}

// readOnlyOp classifies verb+args (args as given to sk, verb included or not).
func readOnlyOp(verb string, args []string) bool {
	if readOnlyVerbs[verb] {
		return true
	}
	subs, ok := readOnlySubs[verb]
	if !ok {
		return false
	}
	pos := positionalArgs(args)
	for i, p := range pos {
		if p == verb {
			pos = pos[i+1:]
			break
		}
	}
	return len(pos) > 0 && subs[pos[0]]
}

// guardPassthrough applies the forbid policy to verbs superkube has no
// enhanced command for (patch, replace, edit, label, annotate, taint, create,
// set, uncordon, exec, debug, cp, run, ...). No confirmation prompts are
// added — passthrough stays verbatim — only forbid rules (and the broken
// config fail-closed rule) apply.
func guardPassthrough(verb string, args []string) error {
	if configErr != nil && !readOnlyOp(verb, args) {
		return requireValidConfig(verb)
	}
	if rule, blocked := Policy().IsForbidden(verb, args); blocked {
		return fmt.Errorf("%s refused by policy %q for context matching %q",
			verb, rule, Policy().MatchedPattern)
	}
	return nil
}

// showPassthroughBanner prints the context banner for passthrough commands,
// which skip cobra's PersistentPreRun. Only on a stderr terminal so piped
// stderr stays clean.
func showPassthroughBanner(stderrTTY bool) {
	if !stderrTTY {
		return
	}
	ui.Init(Flags.Plain)
	showContextBanner()
}
