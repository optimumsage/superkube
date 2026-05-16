// Package guardrail enforces superkube's safety net: typed confirmation for
// destructive operations and dry-run previews for writes. Each enhanced
// command calls into this package explicitly rather than relying on cobra
// middleware, which keeps the call path easy to read and debug.
package guardrail

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"

	"github.com/optimumsage/superkube/internal/ui"
)

// ErrAborted is returned when the user declines a confirmation prompt or the
// typed-confirmation input does not match. Callers should treat it as a
// clean exit (no stack trace, no extra error wrapper).
var ErrAborted = errors.New("aborted")

// YesNo prompts the user with a yes/no confirmation. Returns ErrAborted if
// the user declines. When --yes is set, returns nil immediately. When stdin
// isn't a TTY, returns an error to refuse silent destructive operations.
func YesNo(prompt, detail string, yes bool) error {
	if yes {
		return nil
	}
	if !ui.IsStdinTTY() {
		return fmt.Errorf("%s: refused in non-interactive context, pass --yes to bypass", prompt)
	}
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(prompt).
			Description(detail).
			Affirmative("Yes").
			Negative("Cancel").
			Value(&confirmed),
	))
	if err := form.Run(); err != nil {
		return err
	}
	if !confirmed {
		return ErrAborted
	}
	return nil
}

// TypedName requires the user to type the resource name verbatim before the
// destructive op proceeds. Used for `sk delete pod foo`, `sk drain node-x`,
// etc. — meaningful because a typo or mis-tab can target the wrong resource.
//
// expected is the exact string we'll compare against. detail is shown to the
// user so they know what they're confirming.
func TypedName(detail, expected string, yes bool) error {
	if yes {
		return nil
	}
	if !ui.IsStdinTTY() {
		return fmt.Errorf("typed confirmation for %q refused in non-interactive context, pass --yes to bypass", expected)
	}
	var input string
	prompt := fmt.Sprintf("Type %q to confirm", expected)
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title(prompt).
			Description(detail).
			Value(&input),
	))
	if err := form.Run(); err != nil {
		return err
	}
	if input != expected {
		return fmt.Errorf("input %q did not match %q: %w", input, expected, ErrAborted)
	}
	return nil
}

// TypedPhrase is like TypedName but checks against a fixed phrase such as
// "DELETE" or "DRAIN". Used for `delete --all` and other operations where
// there's no single resource name to anchor against.
func TypedPhrase(detail, phrase string, yes bool) error {
	if yes {
		return nil
	}
	if !ui.IsStdinTTY() {
		return fmt.Errorf("typed phrase %q refused in non-interactive context, pass --yes to bypass", phrase)
	}
	var input string
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title(fmt.Sprintf("Type %q to confirm", phrase)).
			Description(detail).
			Value(&input),
	))
	if err := form.Run(); err != nil {
		return err
	}
	if input != phrase {
		return fmt.Errorf("input %q did not match %q: %w", input, phrase, ErrAborted)
	}
	return nil
}
