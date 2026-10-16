package cli

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/optimumsage/superkube/internal/kubectl"
	"github.com/optimumsage/superkube/internal/ui"
)

func newSecretCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "secret",
		Aliases: []string{"sec", "secrets"},
		Short:   "View or edit Secrets; values are masked by default",
	}
	c.AddCommand(newSecretViewCmd())
	c.AddCommand(newSecretEditCmd())
	return c
}

func newSecretViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "view NAME",
		Short:              "Show a Secret as YAML; values masked unless --reveal",
		Long:               secretViewLongHelp,
		DisableFlagParsing: true,
		RunE:               runSecretView,
	}
}

func newSecretEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "edit NAME",
		Short:              "Open a Secret in $EDITOR; kubectl applies on save",
		Long:               secretEditLongHelp,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResourceEdit(cmd, args, "secret")
		},
	}
}

func runSecretView(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	reveal := hasFlag(args, "--reveal")
	yes := Flags.Yes || hasFlag(args, "--yes") || hasFlag(args, "-y")
	// Strip superkube-only flags before forwarding to kubectl.
	kubectlArgs := stripFlag(args, "--reveal")
	kubectlArgs = stripFlag(kubectlArgs, "--yes")
	kubectlArgs = stripFlag(kubectlArgs, "-y")

	if len(kubectlArgs) == 0 || firstPositional(kubectlArgs) == "" {
		return errors.New("secret view: NAME is required")
	}

	// Non-TTY --reveal requires explicit --yes. A secret leaking into a logged
	// or recorded session is exactly what this guardrail prevents; the explicit
	// --yes is the user's "I accept" handshake.
	if reveal && !ui.IsStdoutTTY() && !yes {
		return errors.New("secret view --reveal in non-TTY mode requires --yes (refusing to write decoded values to a pipe/file by accident)")
	}

	runner, err := kubectl.Default()
	if err != nil {
		return err
	}

	// Capture stdout so we can transform it before printing. kubectl writes
	// its own errors to stderr; we let those through unchanged.
	var stdout bytes.Buffer
	err = runner.Run(cmd.Context(),
		append([]string{"get", "secret"}, append(kubectlArgs, "-o", "yaml")...),
		kubectl.RunOpts{Stdout: &stdout, Stderr: os.Stderr})
	if err != nil {
		// kubectl already wrote the error; propagate exit code without printing
		// partial / empty captured output.
		return err
	}

	out := stdout.String()
	if reveal {
		out = revealSecretYAML(out)
	} else {
		out = maskSecretYAML(out)
	}
	_, _ = fmt.Fprint(cmd.OutOrStdout(), out)
	return nil
}

const (
	secretMaskPlaceholder  = "<base64 hidden — pass --reveal to decode>"
	lastAppliedAnnotation  = "kubectl.kubernetes.io/last-applied-configuration"
	lastAppliedPlaceholder = "<hidden: last-applied-configuration embeds the Secret's data>"
	secretParseFailed      = "# secret view: could not parse kubectl output; refusing to print it unmasked\n"
)

// maskSecretYAML replaces every value under `data:`, `binaryData:` and
// `stringData:` with a masking placeholder, for a single Secret or for each
// item of a List (`sk secret view a b`, `-l ...`). The YAML is parsed
// structurally rather than line-matched so nested/indented items can't slip
// through. Output keys are re-sorted by the marshaller; values are never kept.
// If the input can't be parsed we fail closed rather than echo it.
func maskSecretYAML(raw string) string {
	return transformSecretYAML(raw, func(string) string { return secretMaskPlaceholder }, true)
}

// revealSecretYAML base64-decodes each value in `data:`/`binaryData:` (for a
// single Secret or every List item). Values that fail to decode are replaced
// with `<invalid-base64>`. `stringData` is already plaintext and is left as is.
// The last-applied-configuration annotation is still hidden: it duplicates the
// data and would otherwise show the base64 form alongside the decoded one.
func revealSecretYAML(raw string) string {
	return transformSecretYAML(raw, func(v string) string {
		if v == "" {
			return ""
		}
		dec, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return "<invalid-base64>"
		}
		return string(dec)
	}, false)
}

// transformSecretYAML parses raw (YAML or JSON), applies fn to each
// data/binaryData value (and, when maskStringData, replaces stringData values
// with the mask), hides the last-applied annotation, and re-serialises.
// Documents without any of those fields are returned unchanged.
func transformSecretYAML(raw string, fn func(string) string, maskStringData bool) string {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil || doc == nil {
		if strings.TrimSpace(raw) == "" {
			return raw
		}
		return secretParseFailed
	}
	changed := transformSecretObject(doc, fn, maskStringData)
	if items, ok := doc["items"].([]any); ok {
		for _, it := range items {
			if m, ok := it.(map[string]any); ok && transformSecretObject(m, fn, maskStringData) {
				changed = true
			}
		}
	}
	if !changed {
		return raw
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return secretParseFailed
	}
	return string(out)
}

func transformSecretObject(obj map[string]any, fn func(string) string, maskStringData bool) bool {
	changed := false
	for _, field := range []string{"data", "binaryData"} {
		if m, ok := obj[field].(map[string]any); ok {
			for k, v := range m {
				str, _ := v.(string)
				m[k] = fn(strings.TrimSpace(str))
				changed = true
			}
		}
	}
	if maskStringData {
		if m, ok := obj["stringData"].(map[string]any); ok {
			for k := range m {
				m[k] = secretMaskPlaceholder
				changed = true
			}
		}
	}
	if md, ok := obj["metadata"].(map[string]any); ok {
		if ann, ok := md["annotations"].(map[string]any); ok {
			if _, ok := ann[lastAppliedAnnotation]; ok {
				ann[lastAppliedAnnotation] = lastAppliedPlaceholder
				changed = true
			}
		}
	}
	return changed
}

const secretViewLongHelp = `Show a Secret as YAML.

By default each value under ` + "`data:`" + ` is replaced with a masking placeholder
so the structure is visible but the bytes aren't leaked into your scrollback,
session recording, or screen-share. Pass ` + "`--reveal`" + ` to base64-decode and
print the real values.

` + "`--reveal`" + ` in non-TTY mode (piping, redirecting to a file) requires
` + "`--yes`" + ` to guard against accidentally writing decoded values where you
didn't expect them. Auditing applies in either mode.`

const secretEditLongHelp = `Edit a Secret in your editor.

Wraps ` + "`kubectl edit secret NAME`" + `, which keeps values base64-encoded
in the buffer. To edit a decoded value, base64-decode before editing and
re-encode before saving (or use a stringData entry, which kubectl encodes
for you on apply). Forbid-policy contexts block edit just like ` + "`delete`" + `.`
