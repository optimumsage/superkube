package upgrade

import "testing"

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.2.0", 0},
		{"v0.2.0", "0.2.0", 0},
		{"0.2.0", "v0.2.0", 0},
		{"0.2.0", "0.2.1", -1},
		{"0.2.1", "0.2.0", 1},
		{"0.3.0", "0.2.9", 1},
		{"1.0.0", "0.99.99", 1},
		{"0.2", "0.2.0", 0},
		// Pre-release sorts before its release.
		{"0.2.0-next", "0.2.0", -1},
		{"0.2.0", "0.2.0-next", 1},
		{"0.2.0-alpha", "0.2.0-beta", -1},
		{"0.2.0-alpha", "0.2.0-alpha", 0},
	}
	for _, tc := range cases {
		t.Run(tc.a+"_vs_"+tc.b, func(t *testing.T) {
			got, err := compareSemver(tc.a, tc.b)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("compareSemver(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestCompareSemver_Invalid(t *testing.T) {
	if _, err := compareSemver("not-a-version", "0.2.0"); err == nil {
		t.Error("expected error on non-numeric input")
	}
}

func TestNormalizeTag(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"0.2.0":    "v0.2.0",
		"v0.2.0":   "v0.2.0",
		"  v1.0 ":  "v1.0",
		"1.0":      "v1.0",
		"v0.2.0-x": "v0.2.0-x",
	}
	for in, want := range cases {
		if got := normalizeTag(in); got != want {
			t.Errorf("normalizeTag(%q) = %q, want %q", in, got, want)
		}
	}
}
