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
//   - PEM private-key blocks (whole block, or to end of text if truncated)
//   - JWT-shaped tokens (eyJ... three base64 segments)
//   - "Bearer XXXX" / "Basic XXXX" auth headers
//   - AWS access key IDs (AKIA/ASIA + 16 chars)
//   - the password in URLs with userinfo (scheme://user:PW@host)
//   - YAML/JSON/INI/env values for keys whose names match nameRE (token, key,
//     secret, password, passwd, credential, auth, dockerconfigjson), including
//     dotted keys (tls.key), `export NAME=value`, and mid-line name=value
//   - pod-spec env entries: `- name: DB_PASSWORD` followed by `value: ...`
func Redact(s string) string {
	s = redactBlockScalars(s)
	s = pemRE.ReplaceAllString(s, "<redacted-private-key>")
	s = jwtRE.ReplaceAllString(s, "<redacted-jwt>")
	s = authHeaderRE.ReplaceAllString(s, "${1} <redacted>")
	s = awsKeyIDRE.ReplaceAllString(s, "<redacted-aws-key-id>")
	s = urlUserinfoRE.ReplaceAllString(s, "${1}<redacted>@")
	s = jsonEnvPairRE.ReplaceAllString(s, `${1}"<redacted>"`)
	s = jsonSecretRE.ReplaceAllString(s, `${1}"<redacted>"`)
	s = envNameValueRE.ReplaceAllString(s, "${1}<redacted>")
	s = secretYAMLRE.ReplaceAllStringFunc(s, redactSecretYAML)
	s = secretEnvRE.ReplaceAllStringFunc(s, redactSecretEnv)
	s = inlinePairRE.ReplaceAllString(s, "${1}=<redacted>")
	return s
}

// nameRE is the shared (lowercase-insensitive) credential-name fragment.
const nameRE = `(?:token|key|secret|passw(?:or)?d|credential|auth|dockerconfigjson)`

var (
	pemRE         = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`)
	jwtRE         = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
	authHeaderRE  = regexp.MustCompile(`(?i)(Bearer|Basic)\s+[A-Za-z0-9+/=._-]+`)
	awsKeyIDRE    = regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)
	urlUserinfoRE = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^\s:/@]*:)[^\s@/]+@`)
	// {"password":"x"} / {"apiKey": 123}: quoted JSON key, string or bare value.
	jsonSecretRE = regexp.MustCompile(`(?i)("[A-Za-z0-9_.-]*` + nameRE + `[A-Za-z0-9_.-]*"\s*:\s*)(?:"(?:[^"\\]|\\.)*"|[^\s,}\]]+)`)
	// {"name":"DB_PASSWORD","value":"x"} (pod-spec env as JSON).
	jsonEnvPairRE = regexp.MustCompile(`(?i)("name"\s*:\s*"[A-Za-z0-9_.-]*` + nameRE + `[A-Za-z0-9_.-]*"\s*,\s*"value"\s*:\s*)"(?:[^"\\]|\\.)*"`)
	// - name: DB_PASSWORD\n  value: x (pod-spec env as YAML).
	envNameValueRE = regexp.MustCompile(`(?im)(^[ \t]*-?[ \t]*name:[ \t]*["']?[A-Za-z0-9_.-]*` + nameRE + `[A-Za-z0-9_.-]*["']?[ \t]*\r?\n[ \t]*value:[ \t]*)\S.*$`)
	secretYAMLRE   = regexp.MustCompile(`(?im)^(\s*[A-Za-z0-9_.-]*` + nameRE + `[A-Za-z0-9_.-]*)\s*:\s*(.+)$`)
	// NAME=value, `export NAME=value`, and INI `name = value` at line start.
	secretEnvRE = regexp.MustCompile(`(?im)^(\s*(?:export\s+)?[A-Za-z0-9_.-]*` + nameRE + `[A-Za-z0-9_.-]*)\s*=\s*(.+)$`)
	// name=value anywhere in a line (logs, URLs' query strings).
	inlinePairRE = regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*` + nameRE + `[A-Za-z0-9_.-]*)=("[^"]*"|'[^']*'|[^\s&"',;]+)`)
)

// blockHeaderRE matches `key: |`, `"key": >-`, `- key: |2+ # comment` etc.
// (value is a YAML block scalar whose body is on the following, more-indented
// lines). Groups: 1 indent+optional "- ", 2 open quote, 3 key, 4 close quote.
var (
	blockHeaderRE = regexp.MustCompile(`^(\s*(?:-\s+)?)(["']?)([A-Za-z0-9_.-]+)(["']?)\s*:\s*[|>][+-]?[0-9]?[+-]?\s*(?:#.*)?$`)
	nameKeyRE     = regexp.MustCompile(`(?i)^[A-Za-z0-9_.-]*` + nameRE + `[A-Za-z0-9_.-]*$`)
	envNameLineRE = regexp.MustCompile(`(?i)^\s*-?\s*name:\s*["']?[A-Za-z0-9_.-]*` + nameRE + `[A-Za-z0-9_.-]*["']?\s*$`)
)

// redactBlockScalars masks the body of block scalars whose key is
// credential-named (`tls.key: |`) or is the `value:` of a credential-named
// pod-spec env entry (name either before or after value in the item). The
// line-oriented regexes below only see the header line, so without this the
// indented body would pass through.
func redactBlockScalars(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	prev := ""
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		m := blockHeaderRE.FindStringSubmatch(line)
		if m == nil {
			out = append(out, line)
			prev = line
			continue
		}
		// Body: following blank lines and lines indented deeper than the key.
		indent := len(m[1])
		end := i
		for end+1 < len(lines) {
			next := lines[end+1]
			if strings.TrimSpace(next) != "" && len(next)-len(strings.TrimLeft(next, " \t")) <= indent {
				break
			}
			end++
		}
		sensitive := nameKeyRE.MatchString(m[3])
		if !sensitive && m[3] == "value" {
			sensitive = envNameLineRE.MatchString(prev)
			if !sensitive && end+1 < len(lines) {
				sensitive = envNameLineRE.MatchString(lines[end+1])
			}
		}
		if !sensitive {
			out = append(out, line)
			prev = line
			continue
		}
		out = append(out, m[1]+m[2]+m[3]+m[4]+": <redacted>")
		prev = line
		i = end
	}
	return strings.Join(out, "\n")
}

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
