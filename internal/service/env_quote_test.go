package service

import "testing"

func TestQuoteSystemdEnv(t *testing.T) {
	cases := []struct{ k, v, want string }{
		{"HOME", "/home/m", "HOME=/home/m"},
		{"PATH", "/usr/bin:/opt/My Apps/bin", `"PATH=/usr/bin:/opt/My Apps/bin"`},
		{"X", `a"b`, `"X=a\"b"`},
		{"X", `a\b`, `"X=a\\b"`},
		{"X", "100%", "X=100%%"},
		{"X", "a b%", `"X=a b%%"`},
		{"X", "line1\nline2", `"X=line1 line2"`},
	}
	for _, c := range cases {
		if got := quoteSystemdEnv(c.k, c.v); got != c.want {
			t.Errorf("quoteSystemdEnv(%q,%q) = %s, want %s", c.k, c.v, got, c.want)
		}
	}
}

func TestRenderSystemdQuotesPathWithSpaces(t *testing.T) {
	spec := Spec{Label: "x", BinaryPath: "/bin/sk", Env: map[string]string{"PATH": "/a b:/c"}}
	b, err := RenderSystemdUnit(spec)
	if err != nil {
		t.Fatal(err)
	}
	if want := `Environment="PATH=/a b:/c"`; !contains(string(b), want) {
		t.Fatalf("unit missing %s:\n%s", want, b)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
