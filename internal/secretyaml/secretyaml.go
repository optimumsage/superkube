// Package secretyaml masks or decodes the values in Kubernetes Secret
// manifests (a single Secret or a List of them, YAML or JSON). It parses the
// document structurally instead of line-matching, so indented List items,
// stringData and the last-applied-configuration annotation (which embeds the
// base64 data) cannot slip through. Shared by `sk secret view` and the web UI.
package secretyaml

import (
	"encoding/base64"
	"strings"

	"sigs.k8s.io/yaml"
)

const (
	secretMaskPlaceholder  = "<base64 hidden — pass --reveal to decode>"
	lastAppliedAnnotation  = "kubectl.kubernetes.io/last-applied-configuration"
	lastAppliedPlaceholder = "<hidden: last-applied-configuration embeds the Secret's data>"
	secretParseFailed      = "# secret view: could not parse kubectl output; refusing to print it unmasked\n"
)

// Mask replaces every value under `data:`, `binaryData:` and
// `stringData:` with a masking placeholder, for a single Secret or for each
// item of a List (`sk secret view a b`, `-l ...`). The YAML is parsed
// structurally rather than line-matched so nested/indented items can't slip
// through. Output keys are re-sorted by the marshaller; values are never kept.
// If the input can't be parsed we fail closed rather than echo it.
func Mask(raw string) string {
	return transform(raw, func(string) string { return secretMaskPlaceholder }, true)
}

// Reveal base64-decodes each value in `data:`/`binaryData:` (for a
// single Secret or every List item). Values that fail to decode are replaced
// with `<invalid-base64>`. `stringData` is already plaintext and is left as is.
// The last-applied-configuration annotation is still hidden: it duplicates the
// data and would otherwise show the base64 form alongside the decoded one.
func Reveal(raw string) string {
	return transform(raw, func(v string) string {
		if v == "" {
			return ""
		}
		dec, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return "<invalid-base64>"
		}
		return string(dec)
	}, false)
}

// transform parses raw (YAML or JSON), applies fn to each
// data/binaryData value (and, when maskStringData, replaces stringData values
// with the mask), hides the last-applied annotation, and re-serialises.
// Documents without any of those fields are returned unchanged.
func transform(raw string, fn func(string) string, maskStringData bool) string {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil || doc == nil {
		if strings.TrimSpace(raw) == "" {
			return raw
		}
		return secretParseFailed
	}
	changed := transformObject(doc, fn, maskStringData)
	if items, ok := doc["items"].([]any); ok {
		for _, it := range items {
			if m, ok := it.(map[string]any); ok && transformObject(m, fn, maskStringData) {
				changed = true
			}
		}
	}
	if !changed {
		return raw
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return secretParseFailed
	}
	return string(out)
}

func transformObject(obj map[string]any, fn func(string) string, maskStringData bool) bool {
	changed := false
	for _, field := range []string{"data", "binaryData"} {
		if m, ok := obj[field].(map[string]any); ok {
			for k, v := range m {
				str, _ := v.(string)
				m[k] = fn(strings.TrimSpace(str))
				changed = true
			}
		}
	}
	if maskStringData {
		if m, ok := obj["stringData"].(map[string]any); ok {
			for k := range m {
				m[k] = secretMaskPlaceholder
				changed = true
			}
		}
	}
	if md, ok := obj["metadata"].(map[string]any); ok {
		if ann, ok := md["annotations"].(map[string]any); ok {
			if _, ok := ann[lastAppliedAnnotation]; ok {
				ann[lastAppliedAnnotation] = lastAppliedPlaceholder
				changed = true
			}
		}
	}
	return changed
}
