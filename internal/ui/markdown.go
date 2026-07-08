package ui

import (
	"bytes"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// MarkdownWriter is an io.Writer that applies lightweight, streaming-friendly
// markdown styling to AI output as it arrives. Local AI providers emit markdown
// (headings, **bold**, `code`, bullet lists, fenced code blocks); piped raw to a
// terminal that reads as literal `##`/`**` noise. This wrapper buffers partial
// lines until a newline (model tokens split lines across writes), then styles
// each complete line and forwards it — so the live token-streaming UX is kept
// while the result is readable.
//
// It is deliberately line-local: no width reflow, no table alignment, no
// code-block syntax highlighting. Anything needing the whole document at once
// would force us to buffer the entire response and lose streaming.
//
// When styling is off (non-TTY, --plain, NO_COLOR) it is a transparent
// passthrough, so pipes, jq, and CI keep byte-identical raw markdown.
//
// Like lineColorizer in internal/cli, it sticky-fails: once the downstream
// writer errors, every later Write returns that error so the provider's stdout
// pump unwinds instead of streaming into a dead terminal.
type MarkdownWriter struct {
	w       io.Writer
	buf     []byte
	err     error // sticky
	inFence bool  // inside a ``` / ~~~ fenced code block
	plain   bool  // styling disabled → pure passthrough
}

// NewMarkdownWriter wraps w. Styling is decided once, up front, from Styled():
// a TTY without --plain/NO_COLOR gets styled output; everything else passes
// through untouched.
func NewMarkdownWriter(w io.Writer) *MarkdownWriter {
	return &MarkdownWriter{w: w, buf: make([]byte, 0, 4096), plain: !Styled()}
}

func (m *MarkdownWriter) Write(p []byte) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	if m.plain {
		n, err := m.w.Write(p)
		if err != nil {
			m.err = err
		}
		return n, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	consumed := 0
	rest := p
	for {
		idx := bytes.IndexByte(rest, '\n')
		if idx < 0 {
			m.buf = append(m.buf, rest...)
			consumed += len(rest)
			break
		}
		var line []byte
		if len(m.buf) == 0 {
			line = rest[:idx]
		} else {
			m.buf = append(m.buf, rest[:idx]...)
			line = m.buf
		}
		if _, err := io.WriteString(m.w, m.styleLine(string(line))+"\n"); err != nil {
			m.err = err
			return consumed, err
		}
		consumed += idx + 1
		m.buf = m.buf[:0]
		rest = rest[idx+1:]
		if len(rest) == 0 {
			break
		}
	}
	return consumed, nil
}

// Flush emits any buffered final line (models often end without a trailing
// newline). No newline is appended. Call it after the provider finishes and
// before printing anything else.
func (m *MarkdownWriter) Flush() error {
	if m.err != nil {
		return m.err
	}
	if len(m.buf) == 0 {
		return nil
	}
	out := string(m.buf)
	if !m.plain {
		out = m.styleLine(out)
	}
	_, err := io.WriteString(m.w, out)
	m.buf = m.buf[:0]
	if err != nil {
		m.err = err
	}
	return err
}

// Markdown styles. Basic ANSI colors (12/14) are used so they adapt to the
// user's light/dark terminal theme, matching the rest of the ui palette.
var (
	mdHeading = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	mdBold    = lipgloss.NewStyle().Bold(true)
	mdItalic  = lipgloss.NewStyle().Italic(true)
	mdCode    = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	mdBullet  = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
)

var (
	mdFenceRe   = regexp.MustCompile("^\\s*(```|~~~)")
	mdHeadingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	mdQuoteRe   = regexp.MustCompile(`^\s*>\s?.*$`)
	mdListRe    = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	// A horizontal rule: 3+ of the same marker (RE2 has no backreferences, so
	// each marker gets its own alternative).
	mdHRRe     = regexp.MustCompile(`^\s*(?:-\s*){3,}$|^\s*(?:\*\s*){3,}$|^\s*(?:_\s*){3,}$`)
	mdCodeSpan = regexp.MustCompile("`([^`]+)`")
	mdBoldRe   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdItalicRe = regexp.MustCompile(`\*([^*]+)\*`)
	mdSentinel = regexp.MustCompile("\x00(\\d+)\x00")
)

// styleLine styles one complete markdown line. It is fence-aware: everything
// between ``` fences is dimmed verbatim (never inline-styled) so code isn't
// mangled by the emphasis/code-span passes.
func (m *MarkdownWriter) styleLine(line string) string {
	if mdFenceRe.MatchString(line) {
		m.inFence = !m.inFence
		return Subtle.Render(line)
	}
	if m.inFence {
		return Subtle.Render(line)
	}
	if strings.TrimSpace(line) == "" {
		return line
	}
	if mdHRRe.MatchString(line) {
		return Subtle.Render(line)
	}
	if mm := mdHeadingRe.FindStringSubmatch(line); mm != nil {
		return mdHeading.Render(styleInline(mm[2]))
	}
	if mdQuoteRe.MatchString(line) {
		return Subtle.Render(line)
	}
	if mm := mdListRe.FindStringSubmatch(line); mm != nil {
		indent, marker, content := mm[1], mm[2], mm[3]
		if marker == "-" || marker == "*" || marker == "+" {
			marker = "•"
		}
		return indent + mdBullet.Render(marker) + " " + styleInline(content)
	}
	return styleInline(line)
}

// styleInline applies emphasis/code-span styling within a line. Code spans are
// pulled out first (protected behind NUL sentinels) so a `*` or `_` inside code
// isn't treated as emphasis, then restored last. Only `**bold**`, `*italic*`,
// and “ `code` “ are handled — underscore emphasis is skipped on purpose to
// avoid mangling snake_case identifiers.
func styleInline(s string) string {
	if s == "" {
		return s
	}
	var codes []string
	s = mdCodeSpan.ReplaceAllStringFunc(s, func(tok string) string {
		sub := mdCodeSpan.FindStringSubmatch(tok)
		codes = append(codes, mdCode.Render(sub[1]))
		return "\x00" + strconv.Itoa(len(codes)-1) + "\x00"
	})
	s = mdBoldRe.ReplaceAllStringFunc(s, func(tok string) string {
		return mdBold.Render(mdBoldRe.FindStringSubmatch(tok)[1])
	})
	s = mdItalicRe.ReplaceAllStringFunc(s, func(tok string) string {
		return mdItalic.Render(mdItalicRe.FindStringSubmatch(tok)[1])
	})
	s = mdSentinel.ReplaceAllStringFunc(s, func(tok string) string {
		idx, _ := strconv.Atoi(tok[1 : len(tok)-1])
		if idx >= 0 && idx < len(codes) {
			return codes[idx]
		}
		return tok
	})
	return s
}
