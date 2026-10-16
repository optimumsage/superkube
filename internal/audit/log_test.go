package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestConcurrentRecord proves that flock + sync.Mutex yield a clean JSONL file
// even under parallel writes — no partial lines, no interleaving. This is the
// invariant the entire audit log relies on: jq must be able to parse every
// line.
func TestConcurrentRecord(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	const writers = 8
	const perWriter = 50
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				Record(Event{
					Cmd:  "test",
					Argv: []string{"superkube", "writer", string(rune('A' + id))},
				})
			}
		}(w)
	}
	wg.Wait()

	// XDG resolution in config.StateDir handles the tmp path. Find the log
	// file relative to tmp.
	logPath := filepath.Join(tmp, "superkube", "audit.log")
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer func() { _ = f.Close() }()

	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var e Event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("malformed line %d: %v: %q", count, err, scanner.Text())
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if want := writers * perWriter; count != want {
		t.Errorf("got %d lines, want %d", count, want)
	}
}

func TestRedactArgv_FromLiteral(t *testing.T) {
	got := redactArgv([]string{"create", "secret", "generic", "x", "--from-literal=password=topsecret"})
	if strings.Contains(strings.Join(got, " "), "topsecret") {
		t.Errorf("secret leaked: %v", got)
	}
}

func TestRedactArgv_JWT(t *testing.T) {
	got := redactArgv([]string{"get", "pods", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig"})
	if strings.Contains(strings.Join(got, " "), "eyJhbGci") {
		t.Errorf("JWT leaked: %v", got)
	}
}

func TestRedactArgv_Table(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"from-literal eq", []string{"--from-literal=k=v"}, []string{"--from-literal=k=<redacted>"}},
		{"from-literal space", []string{"--from-literal", "k=v"}, []string{"--from-literal", "k=<redacted>"}},
		{"token eq", []string{"--token=abc"}, []string{"--token=<redacted>"}},
		{"token space", []string{"--token", "abc", "get"}, []string{"--token", "<redacted>", "get"}},
		{"password eq", []string{"--password=pw"}, []string{"--password=<redacted>"}},
		{"password space", []string{"--password", "pw"}, []string{"--password", "<redacted>"}},
		{"docker-password eq", []string{"--docker-password=pw"}, []string{"--docker-password=<redacted>"}},
		{"docker-password space", []string{"--docker-password", "pw"}, []string{"--docker-password", "<redacted>"}},
		{"username kept", []string{"--username", "bob"}, []string{"--username", "bob"}},
		{"client-key path kept", []string{"--client-key", "/p/key"}, []string{"--client-key", "/p/key"}},
		{"trailing flag no value", []string{"--token"}, []string{"--token"}},
	}
	for _, tc := range cases {
		in := append([]string(nil), tc.in...)
		got := redactArgv(in)
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
		if strings.Join(in, "\x00") != strings.Join(tc.in, "\x00") {
			t.Errorf("%s: input mutated", tc.name)
		}
	}
}
