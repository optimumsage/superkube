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
		{"X", "a\r\nb", `"X=a  b"`},
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

func TestQuoteSystemdEscapesPercentAndNewlines(t *testing.T) {
	if got := quoteSystemd("100%"); got != "100%%" {
		t.Errorf("percent: %q", got)
	}
	if got := quoteSystemd("a\nb"); got != `"a b"` {
		t.Errorf("newline: %q", got)
	}
	if got := quoteSystemd("a b%"); got != `"a b%%"` {
		t.Errorf("space+percent: %q", got)
	}
}

func TestRenderSystemdPathDirectivesEscaped(t *testing.T) {
	spec := Spec{Label: "x", BinaryPath: "/bin/sk", LogPath: "/l/100%/o\nInjected=1", ErrLogPath: "/l/e%h"}
	b, err := RenderSystemdUnit(spec)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if contains(s, "\nInjected=1") {
		t.Fatalf("newline injection:\n%s", s)
	}
	if !contains(s, "StandardOutput=append:/l/100%%/o Injected=1") || !contains(s, "StandardError=append:/l/e%%h") {
		t.Fatalf("percent not escaped:\n%s", s)
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
