package ui

import (
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// SectionHeader is the style for top-level keys in `sk describe` output
// (e.g., "Containers:", "Conditions:", "Events:").
var SectionHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))

// orange is reused for Terminating-style states that aren't quite errors but
// aren't healthy either. Not exported because it's only meaningful in context.
var orange = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))

// ColorizeStatus paints a kubectl status string. Mirrors the palette used by
// `sk tui` so the two surfaces feel consistent — Running=green, Pending=warn,
// crash/error/fail/backoff/oom=red bold.
func ColorizeStatus(status string) string {
	if !Styled() || status == "" {
		return status
	}
	switch status {
	case "Running":
		return Success.Render(status)
	case "Pending", "ContainerCreating", "PodInitializing":
		return Warning.Render(status)
	case "Succeeded", "Completed":
		return Info.Render(status)
	case "Terminating":
		return orange.Render(status)
	}
	low := strings.ToLower(status)
	switch {
	case strings.Contains(low, "crash"),
		strings.Contains(low, "error"),
		strings.Contains(low, "fail"),
		strings.Contains(low, "backoff"),
		strings.Contains(low, "oomkill"),
		strings.Contains(low, "invalid"),
		strings.Contains(low, "evicted"):
		return Danger.Render(status)
	case strings.HasPrefix(status, "Init:"):
		return Warning.Render(status)
	}
	return status
}

// ColorizeReady paints READY columns: "3/3" → green, partial → warn, "0/N" or
// "False" → red. Recognized forms are "n/m" and the strings True / False.
func ColorizeReady(value string) string {
	if !Styled() || value == "" {
		return value
	}
	switch value {
	case "True":
		return Success.Render(value)
	case "False":
		return Danger.Render(value)
	case "Unknown":
		return Warning.Render(value)
	}
	if slash := strings.IndexByte(value, '/'); slash > 0 {
		ready, err1 := strconv.Atoi(value[:slash])
		total, err2 := strconv.Atoi(value[slash+1:])
		if err1 != nil || err2 != nil || total == 0 {
			return value
		}
		switch {
		case ready == total:
			return Success.Render(value)
		case ready == 0:
			return Danger.Render(value)
		default:
			return Warning.Render(value)
		}
	}
	return value
}

// ColorizeRestarts paints RESTARTS columns. kubectl emits either a bare
// integer ("3") or the newer "3 (5m ago)" form; we split on the first space
// and grade only the leading count. 0 stays subtle, low counts are warning,
// high counts are danger.
func ColorizeRestarts(value string) string {
	if !Styled() || value == "" {
		return value
	}
	head, _, _ := strings.Cut(value, " ")
	n, err := strconv.Atoi(head)
	if err != nil {
		return value
	}
	switch {
	case n == 0:
		return Subtle.Render(value)
	case n <= 5:
		return Warning.Render(value)
	default:
		return Danger.Render(value)
	}
}

// ColorizeNodeStatus paints the STATUS column from `kubectl get nodes`. The
// column can contain comma-joined states like "Ready,SchedulingDisabled" —
// we colorize the whole cell by the most severe component present.
func ColorizeNodeStatus(value string) string {
	if !Styled() || value == "" {
		return value
	}
	low := strings.ToLower(value)
	switch {
	case strings.Contains(low, "notready"):
		return Danger.Render(value)
	case strings.Contains(low, "schedulingdisabled"):
		return Warning.Render(value)
	case low == "ready":
		return Success.Render(value)
	}
	return value
}

// ColorizeEventType paints the TYPE column in `kubectl get events` and the
// Events: table inside `kubectl describe`. Warning is yellow, Normal is muted.
func ColorizeEventType(value string) string {
	if !Styled() || value == "" {
		return value
	}
	switch value {
	case "Warning":
		return Warning.Render(value)
	case "Normal":
		return Subtle.Render(value)
	}
	return value
}

// ColorizeServiceType paints the TYPE column from `kubectl get svc`.
func ColorizeServiceType(value string) string {
	if !Styled() || value == "" {
		return value
	}
	switch value {
	case "LoadBalancer":
		return Info.Render(value)
	case "NodePort":
		return Warning.Render(value)
	case "ClusterIP", "ExternalName":
		return Subtle.Render(value)
	}
	return value
}

// ColorizeAge paints AGE-shaped values muted so the eye lands on data, not
// timestamps. Used by the get cell renderer and by describe's age fields.
func ColorizeAge(value string) string {
	if !Styled() || value == "" {
		return value
	}
	return Subtle.Render(value)
}

// Log-line patterns. Built lazily because the regexes are non-trivial and not
// every command uses ColorizeLogLine.
var (
	logRegexOnce sync.Once
	rePanic      *regexp.Regexp // "panic:" or "fatal:" at start of line (case-insensitive)
	reLevel      *regexp.Regexp // bracketed or bare level token near line start
	reErrorWord  *regexp.Regexp // word-isolated error/exception/failed
	reHTTPStatus *regexp.Regexp // method + path + status code (common access-log shape)
	reTSPrefix   *regexp.Regexp // ISO-8601 timestamp at line start
	reStackFrame *regexp.Regexp // Java/Python/Go stack frame indents
)

func initLogRegex() {
	logRegexOnce.Do(func() {
		rePanic = regexp.MustCompile(`(?i)^(panic|fatal)[: ]`)
		reLevel = regexp.MustCompile(`(?i)(?:^|\s)\[?(ERROR|ERR|WARNING|WARN|INFO|DEBUG|TRACE|NOTICE)\]?(?:\s|:|$)`)
		reErrorWord = regexp.MustCompile(`(?i)\b(error|errors|exception|failed|failure)\b`)
		reHTTPStatus = regexp.MustCompile(`\b(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\s+\S+\s+(\d{3})\b`)
		reTSPrefix = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)`)
		reStackFrame = regexp.MustCompile(`^(\s+at |\s+File "|\s+at /|\tat |\t/)`)
	})
}

// ColorizeLogLine highlights severity-bearing tokens in a single log line.
// Input must NOT contain a trailing newline (caller controls line framing).
// Returns the line unchanged when !Styled() or when nothing matches, so the
// stream stays byte-identical for the boring 99%.
func ColorizeLogLine(line string) string {
	if !Styled() || line == "" {
		return line
	}
	initLogRegex()

	// Stack frames: whole-line muted. Returns early — no point hunting for
	// other patterns inside.
	if reStackFrame.MatchString(line) {
		return Subtle.Render(line)
	}

	// Whole-line danger for panics/fatals.
	if rePanic.MatchString(line) {
		return Danger.Render(line)
	}

	out := line

	// Timestamp prefix → subtle.
	if loc := reTSPrefix.FindStringIndex(out); loc != nil {
		out = Subtle.Render(out[:loc[1]]) + out[loc[1]:]
	}

	// Level tokens. We pick the strongest level on the line and paint that
	// token only (not the whole line) so multi-field logs stay readable.
	if m := reLevel.FindStringSubmatchIndex(out); m != nil {
		start, end := m[2], m[3]
		level := strings.ToUpper(out[start:end])
		var styled string
		switch level {
		case "ERROR", "ERR":
			styled = Danger.Render(out[start:end])
		case "WARNING", "WARN":
			styled = Warning.Render(out[start:end])
		case "INFO", "NOTICE":
			styled = Info.Render(out[start:end])
		case "DEBUG", "TRACE":
			styled = Subtle.Render(out[start:end])
		}
		if styled != "" {
			out = out[:start] + styled + out[end:]
		}
	} else if m := reErrorWord.FindStringIndex(out); m != nil {
		// No level tag, but a bare "error"/"failed" word — paint just the word.
		out = out[:m[0]] + Danger.Render(out[m[0]:m[1]]) + out[m[1]:]
	}

	// HTTP status. Colorize just the 3-digit code.
	if m := reHTTPStatus.FindStringSubmatchIndex(out); m != nil {
		codeStart, codeEnd := m[4], m[5]
		code, err := strconv.Atoi(out[codeStart:codeEnd])
		if err == nil {
			var styled string
			switch {
			case code >= 500:
				styled = Danger.Render(out[codeStart:codeEnd])
			case code >= 400:
				styled = Warning.Render(out[codeStart:codeEnd])
			case code >= 300:
				styled = Info.Render(out[codeStart:codeEnd])
			case code >= 200:
				styled = Success.Render(out[codeStart:codeEnd])
			}
			if styled != "" {
				out = out[:codeStart] + styled + out[codeEnd:]
			}
		}
	}

	return out
}
