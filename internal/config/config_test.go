package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := ConfigFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileIsFine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := Load()
	if err != nil || c == nil {
		t.Fatalf("Load() = %v, %v; want empty config, nil", c, err)
	}
}

func TestLoadForbidRoundTrip(t *testing.T) {
	writeCfg(t, `
guardrails:
  require_typed_confirm: true
audit:
  enabled: false
ai:
  provider: claude
  timeout: 2m
contexts:
  "prod-*":
    banner: PROD
    require_typed_confirm: true
    forbid: ["delete --all", "drain"]
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	s := c.Contexts["prod-*"]
	if len(s.Forbid) != 2 || s.Forbid[0] != "delete --all" || s.Banner != "PROD" || !s.RequireTypedConfirm {
		t.Errorf("round trip lost data: %+v", s)
	}
	if !c.Guardrails.RequireTypedConfirm || c.Audit.Enabled == nil || *c.Audit.Enabled || c.AI.Provider != "claude" {
		t.Errorf("sections lost: %+v", c)
	}
}

func TestLoadMalformedYAML(t *testing.T) {
	writeCfg(t, "contexts: [unterminated\n  forbid: {")
	if c, err := Load(); err == nil || c != nil {
		t.Fatalf("Load() = %v, %v; want error", c, err)
	}
}

func TestLoadUnknownKeyIsError(t *testing.T) {
	writeCfg(t, "contexts:\n  \"prod-*\":\n    forbids: [delete]\n")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "forbids") {
		t.Fatalf("err = %v; want error naming the unknown key", err)
	}
}

func TestLoadBadTimeoutAndProvider(t *testing.T) {
	writeCfg(t, "ai:\n  timeout: soon\n")
	if _, err := Load(); err == nil {
		t.Error("want error for bad ai.timeout")
	}
	writeCfg(t, "ai:\n  provider: gpt\n")
	if _, err := Load(); err == nil {
		t.Error("want error for unknown ai.provider")
	}
}

func TestLoadEmptyFile(t *testing.T) {
	writeCfg(t, "")
	if _, err := Load(); err != nil {
		t.Fatalf("empty file should load: %v", err)
	}
}
