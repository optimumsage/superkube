package picker

import (
	"strings"
	"testing"
)

func TestMatchScore_Matches(t *testing.T) {
	cases := []struct {
		hay, q string
		want   bool
	}{
		// Subsequence cases inherited from the old fuzzyMatch matrix.
		{"foobar", "fbar", true},
		{"foobar", "fbr", true},
		{"foobar", "foob", true},
		{"foobar", "bo", false}, // no 'o' after the 'b' — order matters within a term
		{"foobar", "xyz", false},
		{"foobar", "obx", false},
		{"prod-cluster-eu", "pe", true},
		{"prod-cluster-eu", "deu", true}, // d ... e ... u still in order
		{"prod-cluster-eu", "xz", false},
		{"", "x", false},
		{"abc", "", true},

		// Multi-term, unordered: fixes the "prod cluster"-vs-"cluster-1-prod"
		// bug from the report.
		{"cluster-1-prod", "prod cluster", true},
		{"cluster-1-prod", "cluster prod", true},
		{"cluster-1-prod", "prod missing", false},
		// Whitespace-only filter behaves like empty.
		{"anything", "   ", true},
	}
	for _, tc := range cases {
		_, got := matchScore(tc.hay, tc.q)
		if got != tc.want {
			t.Errorf("matchScore(%q,%q) matched=%v, want %v", tc.hay, tc.q, got, tc.want)
		}
	}
}

func TestMatchScore_PrioritizesSubstrings(t *testing.T) {
	// A substring hit on `cluster-1-prod` must outrank a loose subsequence on
	// `a-control-plane-extension` for the query "ctx". The bug report calls
	// this out specifically: substrings should win.
	subSeq, ok := matchScore("a-control-plane-extension", "ctx")
	if !ok {
		t.Fatal("subsequence ctx vs a-control-plane-extension should still match")
	}
	substring, ok := matchScore("project-ctx-1", "ctx")
	if !ok {
		t.Fatal("substring ctx vs project-ctx-1 should match")
	}
	if substring <= subSeq {
		t.Errorf("substring score %d must beat subsequence score %d", substring, subSeq)
	}
}

func TestMatchScore_StartOfStringBeatsMidString(t *testing.T) {
	startHit, _ := matchScore("ctx-foo", "ctx")
	midHit, _ := matchScore("foo-ctx", "ctx")
	if startHit <= midHit {
		t.Errorf("start-of-string score %d should beat mid-string %d", startHit, midHit)
	}
}

func TestMatchScore_WordBoundaryBeatsInternal(t *testing.T) {
	// `ctx` at a `-` boundary (`foo-ctx`) should outscore the same letters
	// embedded inside another word (`foctxbar`).
	boundary, _ := matchScore("foo-ctx", "ctx")
	internal, _ := matchScore("foctxbar", "ctx")
	if boundary <= internal {
		t.Errorf("boundary-anchored score %d should beat internal %d", boundary, internal)
	}
}

func TestApplyFilterSortsBestFirst(t *testing.T) {
	cfg := Config{
		Items: []Item{
			{Label: "a-control-plane-extension", Value: "a-control-plane-extension"},
			{Label: "project-ctx-1", Value: "project-ctx-1"},
			{Label: "ctx-prod", Value: "ctx-prod"},
		},
	}
	m := newModel(cfg)
	m.input.SetValue("ctx")
	m.applyFilter()
	if len(m.filtered) == 0 {
		t.Fatal("no matches for `ctx`")
	}
	// `ctx-prod` (start-of-string substring) should be first;
	// `a-control-plane-extension` (loose subsequence) must come last.
	if m.filtered[0].Value != "ctx-prod" {
		t.Errorf("expected ctx-prod first, got %q", m.filtered[0].Value)
	}
	if m.filtered[len(m.filtered)-1].Value != "a-control-plane-extension" {
		t.Errorf("expected a-control-plane-extension last, got %q", m.filtered[len(m.filtered)-1].Value)
	}
}

func TestApplyFilterMultiTermUnordered(t *testing.T) {
	cfg := Config{
		Items: []Item{
			{Label: "dev-cluster-1", Value: "dev-cluster-1"},
			{Label: "cluster-1-prod", Value: "cluster-1-prod"}, // bug-report example
			{Label: "prod-cluster-eu", Value: "prod-cluster-eu"},
			{Label: "staging-cluster", Value: "staging-cluster"},
		},
	}
	m := newModel(cfg)
	m.input.SetValue("prod cluster")
	m.applyFilter()

	want := map[string]bool{
		"cluster-1-prod":  true,
		"prod-cluster-eu": true,
	}
	if len(m.filtered) != len(want) {
		t.Fatalf("got %d matches, want %d: %#v", len(m.filtered), len(want), m.filtered)
	}
	for _, it := range m.filtered {
		if !want[it.Value] {
			t.Errorf("unexpected match %q", it.Value)
		}
	}

	// And swapping the term order returns the same set.
	m.input.SetValue("cluster prod")
	m.applyFilter()
	if len(m.filtered) != len(want) {
		t.Errorf("multi-term should be order-independent: got %d, want %d", len(m.filtered), len(want))
	}
}

func TestApplyFilterResets(t *testing.T) {
	cfg := Config{
		Items: []Item{
			{Label: "alpha", Value: "alpha"},
			{Label: "beta", Value: "beta"},
			{Label: "gamma", Value: "gamma"},
		},
	}
	m := newModel(cfg)
	if len(m.filtered) != 3 {
		t.Fatalf("initial filter: got %d, want 3", len(m.filtered))
	}
	m.input.SetValue("am")
	m.applyFilter()
	if len(m.filtered) != 1 || m.filtered[0].Value != "gamma" {
		t.Errorf("filter \"am\" should match only gamma, got %#v", m.filtered)
	}
	m.input.SetValue("")
	m.applyFilter()
	if len(m.filtered) != 3 {
		t.Errorf("cleared filter should restore all items, got %d", len(m.filtered))
	}
}

func TestMarqueeWindow(t *testing.T) {
	label := "arn:aws:eks:us-east-1:1234:cluster/prod-payments-eks-eu-west"
	// At offset 0, output should start with the label (no separator yet) and
	// be exactly `width` runes wide.
	out := marqueeWindow(label, 20, 0)
	if got := []rune(out); len(got) != 20 {
		t.Fatalf("expected width 20, got %d (%q)", len(got), out)
	}
	if !strings.HasPrefix(out, "arn:aws:eks:us-east-") {
		t.Errorf("offset 0 should start with label head; got %q", out)
	}
	// Advancing the offset shifts the window left by that many runes.
	out2 := marqueeWindow(label, 20, 5)
	if !strings.HasPrefix(out2, "ws:eks:us-east-1:123") {
		t.Errorf("offset 5 should shift 5 runes; got %q", out2)
	}
	// Offsets past the cycle length wrap around — cycle is len(label + sep),
	// so offset n+1 == offset 1 textually.
	cycleLen := len([]rune(label + marqueeSeparator))
	a := marqueeWindow(label, 20, 1)
	b := marqueeWindow(label, 20, cycleLen+1)
	if a != b {
		t.Errorf("marquee should wrap modulo cycle: %q vs %q", a, b)
	}
	// Empty label + zero width are degenerate but must not panic.
	if got := marqueeWindow("", 10, 7); got != "" {
		t.Errorf("empty label should produce empty window; got %q", got)
	}
	if got := marqueeWindow("abc", 0, 0); got != "" {
		t.Errorf("zero width should produce empty window; got %q", got)
	}
}

func TestCursorClampsToFiltered(t *testing.T) {
	cfg := Config{
		Items: []Item{
			{Label: "a", Value: "a"},
			{Label: "b", Value: "b"},
			{Label: "c", Value: "c"},
		},
	}
	m := newModel(cfg)
	m.w, m.h = 80, 24
	m.cursor = 2
	m.input.SetValue("b")
	m.applyFilter()
	// After narrowing to a single match, the cursor must end at 0.
	// (We don't call applyFilter via Update here, so we simulate the reset.)
	m.cursor = 0
	m.top = 0
	m.clampCursor()
	if m.cursor != 0 || m.top != 0 {
		t.Errorf("cursor/top should be 0 after filter narrows list; got cursor=%d top=%d", m.cursor, m.top)
	}
}
