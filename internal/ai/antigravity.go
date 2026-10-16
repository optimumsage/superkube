package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// antigravityProvider shells out to the Antigravity CLI's `agy -p` (print) mode.
// It replaced the old Gemini provider when the Gemini CLI was discontinued.
type antigravityProvider struct{}

func (antigravityProvider) Name() string { return "antigravity" }

func (antigravityProvider) Available(_ context.Context) bool {
	_, err := exec.LookPath("agy")
	return err == nil
}

func (antigravityProvider) VersionString(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "agy", "--version")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return "antigravity " + strings.TrimSpace(string(out))
}

// Run streams Antigravity's output to w.
//
// The prompt is delivered on stdin using agy's stream-json input mode rather
// than as `-p "<prompt>"`: an argv element is capped at 128 KiB on Linux
// (MAX_ARG_STRLEN), so large diagnose payloads (describe + logs) would fail
// with E2BIG, and argv is world-readable via `ps`. Verified against the real
// binary: `agy -p -` treats "-" as a literal prompt and `agy -p` with no value
// is a usage error, whereas
//
//	agy --input-format stream-json --output-format stream-json
//
// reads one {"event":"user","message":{"content":"..."}} line per turn from
// stdin and emits NDJSON events on stdout. We forward each agent_response
// text_delta to w as it arrives, so output still streams.
//
// opts.AllowReadOnlyKubectl is honored on a best-effort, prompt-only basis:
// agy has no per-command tool allowlist (unlike claude's --allowedTools), and
// we deliberately do NOT pass --dangerously-skip-permissions because that would
// auto-approve state-mutating tools too, violating superkube's guardrail model.
// The prompt templates (prompt.go) are what constrain the model to read-only
// verbs for this provider.
func (antigravityProvider) Run(ctx context.Context, prompt string, w io.Writer, _ RunOpts) error {
	if w == nil {
		return errors.New("antigravity.Run: nil writer")
	}
	msg, err := json.Marshal(agyUserEvent{
		Event:   "user",
		Message: agyUserMessage{Content: prompt},
	})
	if err != nil {
		return fmt.Errorf("antigravity: encode prompt: %w", err)
	}
	cmd := exec.CommandContext(ctx, "agy", "--input-format", "stream-json", "--output-format", "stream-json")
	cmd.Stdin = bytes.NewReader(append(msg, '\n'))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("antigravity: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("antigravity: %w", err)
	}
	streamed, resultErr, relayErr := relayAgyEvents(stdout, w)
	if relayErr != nil {
		// Output was truncated (writer failed) or the stream broke: stop agy
		// and reap it so we don't leak a process, then report the real cause.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("antigravity: %w", relayErr)
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if resultErr != nil {
			// agy reports the useful reason in its result event, not stderr.
			if detail != "" {
				detail = resultErr.Error() + "; " + detail
			} else {
				detail = resultErr.Error()
			}
		}
		if detail != "" {
			return fmt.Errorf("antigravity: %w (%s)", waitErr, detail)
		}
		return fmt.Errorf("antigravity: %w", waitErr)
	}
	if resultErr != nil {
		return fmt.Errorf("antigravity: %w", resultErr)
	}
	if !streamed {
		return errors.New("antigravity: no response received")
	}
	return nil
}

type agyUserEvent struct {
	Event   string         `json:"event"`
	Message agyUserMessage `json:"message"`
}

type agyUserMessage struct {
	Content string `json:"content"`
}

type agyEvent struct {
	Event      string `json:"event"`
	StepUpdate struct {
		StepType  string `json:"step_type"`
		TextDelta string `json:"text_delta"`
	} `json:"step_update"`
	Result struct {
		Status   string `json:"status"`
		Response string `json:"response"`
		Error    string `json:"error"`
	} `json:"result"`
}

// relayAgyEvents copies agent_response text deltas from agy's NDJSON stream to
// w. It reports whether any text was written and, if agy's final result event
// carried an error status, that error. relayErr is the first write error to w
// or non-EOF read error; the caller must stop the subprocess when it is set. If
// no deltas arrived but the result has a response, the response is written
// instead.
func relayAgyEvents(r io.Reader, w io.Writer) (wrote bool, resultErr, relayErr error) {
	write := func(text string) bool {
		if _, err := io.WriteString(w, text); err != nil {
			relayErr = fmt.Errorf("write output: %w", err)
			return false
		}
		wrote = true
		return true
	}
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var ev agyEvent
			if json.Unmarshal(line, &ev) == nil {
				switch ev.Event {
				case "step_update":
					if ev.StepUpdate.StepType == "agent_response" && ev.StepUpdate.TextDelta != "" {
						if !write(ev.StepUpdate.TextDelta) {
							return wrote, resultErr, relayErr
						}
					}
				case "result":
					if ev.Result.Status != "" && ev.Result.Status != "SUCCESS" {
						msg := ev.Result.Error
						if msg == "" {
							msg = "status " + ev.Result.Status
						}
						resultErr = errors.New(msg)
					} else if !wrote && ev.Result.Response != "" {
						if !write(ev.Result.Response) {
							return wrote, resultErr, relayErr
						}
					}
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				relayErr = fmt.Errorf("read agy output: %w", err)
			}
			return wrote, resultErr, relayErr
		}
	}
}
