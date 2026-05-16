package config

import (
	"os"

	"sigs.k8s.io/yaml"
)

// Config is the parsed shape of ~/.config/superkube/config.yaml. Every field
// is optional; missing fields use the zero value, which the runtime treats as
// "no override".
type Config struct {
	AI         AISection                 `json:"ai,omitempty"`
	Audit      AuditSection              `json:"audit,omitempty"`
	Guardrails GuardrailsSection         `json:"guardrails,omitempty"`
	Contexts   map[string]ContextSection `json:"contexts,omitempty"`
}

type AISection struct {
	Provider string `json:"provider,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
}

type AuditSection struct {
	Enabled *bool `json:"enabled,omitempty"`
}

type GuardrailsSection struct {
	// Default require_typed_confirm for ALL contexts (off by default).
	RequireTypedConfirm bool `json:"require_typed_confirm,omitempty"`
}

// ContextSection is keyed by a glob pattern that matches one or more kubectl
// context names (e.g. "prod-*"). At command-eval time, each rule whose
// pattern matches the current context contributes to the effective policy
// (forbids union; require_typed_confirm OR'd).
type ContextSection struct {
	RequireTypedConfirm bool     `json:"require_typed_confirm,omitempty"`
	Forbid              []string `json:"forbid,omitempty"`
	// Banner overrides the default banner text shown at the top of every
	// command in this context. Leave empty to show the auto-generated label.
	Banner string `json:"banner,omitempty"`
}

// Load reads and parses ConfigFile(). Returns an empty &Config{} when the file
// doesn't exist (the most common case for first-time users) so callers can
// always rely on the pointer being non-nil.
func Load() (*Config, error) {
	path := ConfigFile()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}
