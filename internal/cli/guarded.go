package cli

import (
	"fmt"

	"github.com/optimumsage/superkube/internal/config"
	"github.com/optimumsage/superkube/internal/guardrail"
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

// readOnlyVerbs are kubectl verbs that never mutate cluster state. They keep
// working when config.yaml is broken (with the warning); every other
// passthrough verb — including unknown krew plugins — fails closed.
var readOnlyVerbs = map[string]bool{
	"get": true, "describe": true, "logs": true, "top": true, "explain": true,
	"api-resources": true, "api-versions": true, "version": true, "config": true,
	"cluster-info": true, "completion": true, "events": true, "options": true,
	"help": true, "diff": true, "wait": true, "auth": true, "plugin": true,
	"kustomize": true,
}

// guardPassthrough applies the forbid policy to verbs superkube has no
// enhanced command for (patch, replace, edit, label, annotate, taint, create,
// set, uncordon, exec, debug, cp, run, ...). No confirmation prompts are
// added — passthrough stays verbatim — only forbid rules (and the broken
// config fail-closed rule) apply.
func guardPassthrough(verb string, args []string) error {
	if configErr != nil && !readOnlyVerbs[verb] {
		return requireValidConfig(verb)
	}
	if rule, blocked := Policy().IsForbidden(verb, args); blocked {
		return fmt.Errorf("%s refused by policy %q for context matching %q",
			verb, rule, Policy().MatchedPattern)
	}
	return nil
}
