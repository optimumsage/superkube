package cli

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

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

// maskSecretYAML replaces each value in the top-level `data:` block with a
// masking placeholder. We deliberately keep the YAML structure intact (keys,
// indentation, ordering) so the output still reads naturally and can be
// diff'd against a known structure.
//
// kubectl always emits two-space indents for the data block in v1; we look
// for lines that match `  <key>: <value>` *inside* a `data:` block. The
// nearby keys (`metadata`, `kind`, `apiVersion`, `stringData`, etc.) live at
// indent 0 or under different parents, so the block boundary is clear: a
// non-indented or differently-indented line ends the block.
func maskSecretYAML(raw string) string {
	return transformSecretYAML(raw, func(key, value string) string {
		_ = key
		_ = value
		return "<base64 hidden — pass --reveal to decode>"
	})
}

// revealSecretYAML base64-decodes each value in the top-level `data:` block.
// Values that fail to decode are replaced with `<invalid-base64>` rather than
// panicking — a non-base64 value in `data:` would already be a kubectl bug.
//
// Multi-line decoded values are emitted as a YAML block scalar (`|`) so the
// output remains valid YAML.
func revealSecretYAML(raw string) string {
	return transformSecretYAML(raw, func(key, value string) string {
		v := strings.TrimSpace(value)
		// kubectl emits empty strings as `""` — preserve that for readability.
		if v == "\"\"" || v == "''" || v == "" {
			return "\"\""
		}
		// Strip optional quotes kubectl adds around base64 values.
		v = strings.Trim(v, "\"'")
		dec, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return "<invalid-base64>"
		}
		if !strings.ContainsRune(string(dec), '\n') {
			return yamlScalar(string(dec))
		}
		// Multi-line: emit as block scalar so we don't have to escape every
		// special character. Indentation matches the data block (4 spaces here
		// because the data key itself sits at indent 2).
		lines := strings.Split(string(dec), "\n")
		var sb strings.Builder
		sb.WriteString("|\n")
		for _, l := range lines {
			sb.WriteString("    ")
			sb.WriteString(l)
			sb.WriteString("\n")
		}
		// Trim trailing newline added by the loop; YAML will re-add one when
		// the line is written back.
		return strings.TrimRight(sb.String(), "\n")
	})
}

// transformSecretYAML walks raw line-by-line and applies fn to every `data:`
// (and `binaryData:`) child value. The transform receives the raw key + value
// strings and returns the replacement value (no surrounding key/indent).
//
// We don't pull in a full YAML parser here: kubectl's output for Secrets is
// stable and shallow (a single nested map under `data:`/`binaryData:`), so a
// scan based on indentation is sufficient and avoids a dep.
func transformSecretYAML(raw string, fn func(key, value string) string) string {
	if !strings.Contains(raw, "\ndata:") && !strings.HasPrefix(raw, "data:") &&
		!strings.Contains(raw, "\nbinaryData:") && !strings.HasPrefix(raw, "binaryData:") {
		return raw
	}
	lines := strings.SplitAfter(raw, "\n")
	var sb strings.Builder
	sb.Grow(len(raw))
	inDataBlock := false
	for _, line := range lines {
		stripped := strings.TrimRight(line, "\n")
		switch {
		case stripped == "data:" || stripped == "binaryData:":
			inDataBlock = true
			sb.WriteString(line)
			continue
		case strings.HasPrefix(stripped, "data: ") || strings.HasPrefix(stripped, "binaryData: "):
			// `data: {}` (empty map) — nothing to mask, block ends immediately.
			inDataBlock = false
			sb.WriteString(line)
			continue
		}
		if !inDataBlock {
			sb.WriteString(line)
			continue
		}
		// Block ends when we hit a line whose indentation drops back to zero or
		// a different top-level key.
		if stripped == "" || !strings.HasPrefix(line, "  ") {
			inDataBlock = false
			sb.WriteString(line)
			continue
		}
		key, value, ok := splitDataLine(stripped)
		if !ok {
			sb.WriteString(line)
			continue
		}
		sb.WriteString("  ")
		sb.WriteString(key)
		sb.WriteString(": ")
		sb.WriteString(fn(key, value))
		sb.WriteString("\n")
	}
	return sb.String()
}

// splitDataLine pulls (key, value) out of a `  key: value` line. Returns
// ok=false if the line isn't a key/value pair (e.g. a continuation of a
// block scalar). Keys are bare identifiers in kubectl's output, so we don't
// need to handle quoted keys.
func splitDataLine(s string) (key, value string, ok bool) {
	trimmed := strings.TrimLeft(s, " ")
	idx := strings.Index(trimmed, ":")
	if idx <= 0 {
		return "", "", false
	}
	key = trimmed[:idx]
	rest := trimmed[idx+1:]
	value = strings.TrimLeft(rest, " ")
	return key, value, true
}

// yamlScalar quotes value if it could be misread as a YAML special token
// (numbers, bools, leading whitespace, special chars). For most secret
// payloads (URLs, JWTs, passwords) double-quoting is the safe bet.
func yamlScalar(v string) string {
	if v == "" {
		return "\"\""
	}
	// Reserved special tokens or values that look numeric/boolean would
	// otherwise change type after a round-trip; quote them.
	switch strings.ToLower(v) {
	case "true", "false", "null", "yes", "no", "on", "off", "~":
		return fmt.Sprintf("%q", v)
	}
	// Heuristic: contains characters that could confuse a YAML parser.
	if strings.ContainsAny(v, ":#&*!|>%@`,[]{}\"\\") || strings.HasPrefix(v, " ") || strings.HasSuffix(v, " ") {
		return fmt.Sprintf("%q", v)
	}
	return v
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
