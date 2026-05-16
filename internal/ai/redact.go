package ai

import (
	"regexp"
	"strings"
)

// Redact masks credential-shaped values in a free-form text blob before we
// hand it to a model. This is best-effort, not security — a determined user
// can still leak data via the prompt itself. Document that explicitly.
//
// What we mask:
//   - JWT-shaped tokens (eyJ... three base64 segments)
//   - "Bearer XXXX" / "Basic XXXX" auth headers
//   - YAML/JSON values for keys whose names match (?i)(token|key|secret|
//     password|credential|auth)
//   - environment-variable lines (KEY=VALUE) where KEY matches the same regex
func Redact(s string) string {
	s = jwtRE.ReplaceAllString(s, "<redacted-jwt>")
	s = authHeaderRE.ReplaceAllString(s, "${1} <redacted>")
	s = secretYAMLRE.ReplaceAllStringFunc(s, redactSecretYAML)
	s = secretEnvRE.ReplaceAllStringFunc(s, redactSecretEnv)
	return s
}

var (
	jwtRE        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
	authHeaderRE = regexp.MustCompile(`(?i)(Bearer|Basic)\s+[A-Za-z0-9+/=._-]+`)
	secretYAMLRE = regexp.MustCompile(`(?im)^(\s*[A-Za-z0-9_-]*(?:token|key|secret|password|credential|auth)[A-Za-z0-9_-]*)\s*:\s*(.+)$`)
	secretEnvRE  = regexp.MustCompile(`(?im)^([A-Za-z0-9_]*(?:TOKEN|KEY|SECRET|PASSWORD|CREDENTIAL|AUTH)[A-Za-z0-9_]*)=(.+)$`)
)

func redactSecretYAML(line string) string {
	m := secretYAMLRE.FindStringSubmatch(line)
	if len(m) != 3 {
		return line
	}
	// Preserve indentation + key, mask value.
	keyPart := m[1]
	return keyPart + ": <redacted>"
}

func redactSecretEnv(line string) string {
	m := secretEnvRE.FindStringSubmatch(line)
	if len(m) != 3 {
		return line
	}
	return m[1] + "=<redacted>"
}

// TruncateLogs keeps the trailing N lines of a log buffer; the head is dropped
// with a marker so the model knows there was more. Used for `--ai` log
// analysis and `sk diagnose` payloads.
func TruncateLogs(s string, maxLines int) string {
	if maxLines <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	skipped := len(lines) - maxLines
	tail := lines[skipped:]
	return "[... " + itoa(skipped) + " earlier lines truncated ...]\n" + strings.Join(tail, "\n")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
