package ai

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeAgy installs a fake `agy` on PATH whose body is the given shell
// script, and returns nothing — Run() locates it via exec.LookPath.
func writeFakeAgy(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake agy requires a POSIX shell")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agy"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("write fake agy: %v", err)
	}
	t.Setenv("PATH", dir)
}

func TestAntigravityRunStreamsOutputAndDeliversPromptOnStdin(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "capture")
	t.Setenv("AGY_CAPTURE", capture)
	// Only shell builtins: PATH is replaced with the fake's dir.
	writeFakeAgy(t, `read -r line
printf '%s\n' "$line" > "$AGY_CAPTURE.stdin"
printf '%s\n' "$*" > "$AGY_CAPTURE.args"
printf '%s\n' '{"event":"init"}'
printf '%s\n' '{"event":"step_update","step_update":{"step_type":"user_input","state":"DONE"}}'
printf '%s\n' '{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"answer "}}'
printf '%s\n' '{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"here"}}'
printf '%s\n' '{"event":"result","result":{"status":"SUCCESS","response":"answer here"}}'
`)

	var out bytes.Buffer
	if err := (antigravityProvider{}).Run(context.Background(), "why is my pod pending", &out, RunOpts{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); got != "answer here" {
		t.Fatalf("output = %q", got)
	}
	args, _ := os.ReadFile(capture + ".args")
	if strings.Contains(string(args), "why is my pod pending") {
		t.Errorf("prompt leaked into argv: %q", args)
	}
	stdin, _ := os.ReadFile(capture + ".stdin")
	if !strings.Contains(string(stdin), "why is my pod pending") || !strings.Contains(string(stdin), `"event":"user"`) {
		t.Errorf("prompt not delivered as stream-json on stdin: %q", stdin)
	}
}

func TestAntigravityRunResultError(t *testing.T) {
	writeFakeAgy(t, `read -r line
printf '%s\n' '{"event":"result","result":{"status":"ERROR","error":"quota exceeded"}}'
exit 1
`)
	var out bytes.Buffer
	err := (antigravityProvider{}).Run(context.Background(), "hi", &out, RunOpts{})
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("expected result error, got %v", err)
	}
}

func TestAntigravityRunFallsBackToResultResponse(t *testing.T) {
	writeFakeAgy(t, `read -r line
printf '%s\n' '{"event":"result","result":{"status":"SUCCESS","response":"only final"}}'
`)
	var out bytes.Buffer
	if err := (antigravityProvider{}).Run(context.Background(), "hi", &out, RunOpts{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "only final" {
		t.Fatalf("got %q", out.String())
	}
}

func TestAntigravityRunSurfacesStderr(t *testing.T) {
	writeFakeAgy(t, `echo "model unavailable" >&2; exit 3`)

	var out bytes.Buffer
	err := (antigravityProvider{}).Run(context.Background(), "hi", &out, RunOpts{})
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("error %q should surface stderr", err)
	}
}

func TestAntigravityRunNilWriter(t *testing.T) {
	if err := (antigravityProvider{}).Run(context.Background(), "hi", nil, RunOpts{}); err == nil {
		t.Fatal("expected error for nil writer")
	}
}

// failAfterWriter succeeds for the first n writes, then errors.
type failAfterWriter struct {
	n   int
	buf bytes.Buffer
}

func (f *failAfterWriter) Write(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("disk full")
	}
	f.n--
	return f.buf.Write(p)
}

func TestAntigravityRunPropagatesWriterError(t *testing.T) {
	script := `read -r line
printf '%s\n' '{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"one"}}'
printf '%s\n' '{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"two"}}'
printf '%s\n' '{"event":"result","result":{"status":"SUCCESS","response":"onetwo"}}'
`
	for _, n := range []int{1, 0} {
		writeFakeAgy(t, script)
		err := (antigravityProvider{}).Run(context.Background(), "hi", &failAfterWriter{n: n}, RunOpts{})
		if err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("n=%d: expected writer error, got %v", n, err)
		}
	}
}
