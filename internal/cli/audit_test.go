package cli

import (
	"testing"
	"time"

	"github.com/optimumsage/superkube/internal/audit"
)

func TestMatchAuditFilters(t *testing.T) {
	now := time.Now()
	e := audit.Event{
		Timestamp: now,
		Verb:      "delete",
		Context:   "prod-eu-west-1",
		Namespace: "kube-system",
		ExitCode:  0,
	}
	fail := e
	fail.ExitCode = 1
	old := e
	old.Timestamp = now.Add(-2 * time.Hour)

	cases := []struct {
		name   string
		event  audit.Event
		f      auditFilters
		cutoff time.Time
		want   bool
	}{
		{"no filters match", e, auditFilters{}, time.Time{}, true},
		{"verb match", e, auditFilters{verb: "delete"}, time.Time{}, true},
		{"verb mismatch", e, auditFilters{verb: "apply"}, time.Time{}, false},
		{"context substring match", e, auditFilters{context: "prod"}, time.Time{}, true},
		{"context mismatch", e, auditFilters{context: "staging"}, time.Time{}, false},
		{"failed filters out success", e, auditFilters{failed: true}, time.Time{}, false},
		{"failed keeps failure", fail, auditFilters{failed: true}, time.Time{}, true},
		{"cutoff drops old", old, auditFilters{}, now.Add(-time.Hour), false},
		{"cutoff keeps fresh", e, auditFilters{}, now.Add(-time.Hour), true},
		{"compound match", fail, auditFilters{verb: "delete", failed: true, context: "prod"}, time.Time{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchAuditFilters(tc.event, tc.f, tc.cutoff)
			if got != tc.want {
				t.Errorf("matchAuditFilters = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTopN(t *testing.T) {
	m := map[string]int{
		"apply":  5,
		"delete": 3,
		"get":    10,
		"logs":   1,
	}
	out := topN(m, 3)
	if len(out) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(out))
	}
	if out[0].key != "get" || out[0].n != 10 {
		t.Errorf("expected get=10 first, got %v", out[0])
	}
	if out[1].key != "apply" || out[1].n != 5 {
		t.Errorf("expected apply=5 second, got %v", out[1])
	}
}

func TestTruncate(t *testing.T) {
	if truncate("short", 10) != "short" {
		t.Error("short string unexpectedly truncated")
	}
	if got := truncate("verylongstring", 6); got != "veryl…" {
		t.Errorf("truncate to 6 got %q", got)
	}
}
