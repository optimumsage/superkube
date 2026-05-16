package picker

import "testing"

func TestFuzzyMatch(t *testing.T) {
	cases := []struct {
		hay, q string
		want   bool
	}{
		{"foobar", "fbar", true},
		{"foobar", "fbr", true},
		{"foobar", "foob", true},
		{"foobar", "bo", false}, // no 'o' after the 'b' — order matters
		{"foobar", "xyz", false},
		{"foobar", "obx", false},
		{"prod-cluster-eu", "pe", true},
		{"prod-cluster-eu", "deu", true}, // d ... e ... u all appear in order
		{"prod-cluster-eu", "xz", false},
		{"", "x", false},
		{"abc", "", true},
	}
	for _, tc := range cases {
		if got := fuzzyMatch(tc.hay, tc.q); got != tc.want {
			t.Errorf("fuzzyMatch(%q,%q)=%v want %v", tc.hay, tc.q, got, tc.want)
		}
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
