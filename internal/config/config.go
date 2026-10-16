package config

import (
	"fmt"
	"os"
	"strings"
	"time"

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
// always rely on the pointer being non-nil on success.
//
// Parsing is strict: unknown keys (a typo such as `forbids:`) and malformed
// YAML are errors. A guardrail config that is silently half-ignored fails
// open, which is the worst failure mode for a safety tool; callers are
// expected to refuse guarded operations when Load errors.
func Load() (*Config, error) {
	path := ConfigFile()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	return Parse(data)
}

// Parse strictly parses and validates config YAML (unknown keys, malformed
// YAML and bad ai.* values are errors). Load uses it, and anything that edits
// the config (e.g. the web config editor) should validate through it so the
// two can never disagree about what is acceptable.
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// AITimeout returns the parsed ai.timeout, or 0 when unset.
func (c *Config) AITimeout() time.Duration {
	d, _ := time.ParseDuration(c.AI.Timeout)
	return d
}

func (c *Config) validate() error {
	switch strings.ToLower(c.AI.Provider) {
	case "", "claude", "antigravity", "agy":
	default:
		return fmt.Errorf("ai.provider %q is not supported (use claude or antigravity)", c.AI.Provider)
	}
	if c.AI.Timeout != "" {
		d, err := time.ParseDuration(c.AI.Timeout)
		if err != nil || d <= 0 {
			return fmt.Errorf("ai.timeout %q is not a positive duration (e.g. 90s, 2m)", c.AI.Timeout)
		}
	}
	return nil
}
