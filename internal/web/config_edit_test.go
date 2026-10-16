package web

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/optimumsage/superkube/internal/config"
)

func putConfig(e *hardenEnv, t *testing.T, body string) *http.Response {
	t.Helper()
	return e.doMethod(t, "PUT", "/api/v1/config", body).Result()
}

func TestConfigPutNeedsTypedConfirmAndIsAudited(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := config.ConfigFile()
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := "contexts:\n  prod:\n    forbid: [\"delete\"]\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newHardenEnv(t, hardenOpts{yes: true}) // server --yes must not skip this
	newYAML := "audit:\n  enabled: true\n"
	body := mustJSONStr(map[string]string{"yaml": newYAML})

	rr := e.doMethod(t, "PUT", "/api/v1/config", body)
	first := decode[confirmationResponse](t, rr)
	if first.Status != "needs_confirmation" || first.Expect != "EDIT CONFIG" {
		t.Fatalf("first = %+v", first)
	}
	if b, _ := os.ReadFile(path); string(b) != old {
		t.Fatal("file written before confirmation")
	}
	wrong := mustJSONStr(map[string]string{"yaml": newYAML, "confirm_token": first.Token, "confirm_value": "yes"})
	if rr := e.doMethod(t, "PUT", "/api/v1/config", wrong); rr.Code != http.StatusGone {
		t.Fatalf("wrong phrase: %d", rr.Code)
	}
	first = decode[confirmationResponse](t, e.doMethod(t, "PUT", "/api/v1/config", body))
	// A token cannot be reused for different content.
	other := mustJSONStr(map[string]string{"yaml": "audit:\n  enabled: false\n", "confirm_token": first.Token, "confirm_value": "EDIT CONFIG"})
	if rr := e.doMethod(t, "PUT", "/api/v1/config", other); rr.Code != http.StatusGone {
		t.Fatalf("token reuse: %d", rr.Code)
	}
	first = decode[confirmationResponse](t, e.doMethod(t, "PUT", "/api/v1/config", body))
	good := mustJSONStr(map[string]string{"yaml": newYAML, "confirm_token": first.Token, "confirm_value": "EDIT CONFIG"})
	if rr := e.doMethod(t, "PUT", "/api/v1/config", good); rr.Code != http.StatusOK {
		t.Fatalf("confirmed: %d %s", rr.Code, rr.Body.String())
	}
	if b, _ := os.ReadFile(path); string(b) != newYAML {
		t.Fatalf("file = %q", b)
	}
	var found bool
	for _, ev := range e.events {
		if ev.Verb == "config-edit" && strings.Contains(strings.Join(ev.Argv, " "), "changed: audit,contexts") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no config-edit audit with summary: %+v", e.events)
	}
}

func TestConfigPutRejectsUnknownKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	e := newHardenEnv(t, hardenOpts{})
	for name, y := range map[string]string{
		"unknown top key": "bogus: 1\n",
		"typo in context": "contexts:\n  prod:\n    forbidd: [delete]\n",
		"wrong type":      "guardrails:\n  require_typed_confirm: banana\n",
		"not yaml":        "a: [\n",
	} {
		if rr := e.doMethod(t, "PUT", "/api/v1/config", mustJSONStr(map[string]string{"yaml": y})); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: code %d, want 400", name, rr.Code)
		}
	}
}
