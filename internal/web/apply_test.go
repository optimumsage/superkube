package web

import (
	"strings"
	"testing"
)

func TestDiffToHTMLColorizesAddAndDel(t *testing.T) {
	raw := "--- a\n+++ b\n@@ -1,1 +1,1 @@\n-old\n+new\n unchanged\n"
	html := diffToHTML(raw)
	for _, want := range []string{
		`class="diff-line head"`,
		`class="diff-line hunk"`,
		`class="diff-line add"`,
		`class="diff-line del"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("html missing %q: %s", want, html)
		}
	}
}

func TestHTMLEscape(t *testing.T) {
	got := htmlEscape(`<a href="x">&</a>`)
	want := `&lt;a href=&quot;x&quot;&gt;&amp;&lt;/a&gt;`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
