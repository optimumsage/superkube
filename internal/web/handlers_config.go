package web

import (
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/optimumsage/superkube/internal/config"
)

// apiConfigGet returns the current config.yaml contents plus its path.
func (s *Server) apiConfigGet(w http.ResponseWriter, _ *http.Request) {
	path := config.ConfigFile()
	body := ""
	if f, err := os.Open(path); err == nil {
		b, _ := io.ReadAll(f)
		_ = f.Close()
		body = string(b)
	}
	s.render.JSON(w, http.StatusOK, map[string]string{
		"path": path,
		"yaml": body,
	})
}

// parseConfig validates config.yaml text strictly: unknown keys and wrong types
// are errors, so a typo in a `forbid:` section is rejected instead of silently
// dropping the rule.
// TODO(integration): config.Parse
func parseConfig(data []byte) error {
	var probe config.Config
	return yaml.UnmarshalStrict(data, &probe)
}

// configChangeSummary names the top-level keys whose value differs between the
// old and new document, e.g. "changed: contexts,guardrails".
func configChangeSummary(oldData, newData []byte) string {
	var o, n map[string]any
	_ = yaml.Unmarshal(oldData, &o)
	_ = yaml.Unmarshal(newData, &n)
	seen := map[string]bool{}
	var keys []string
	for k := range o {
		seen[k] = true
	}
	for k := range n {
		seen[k] = true
	}
	for k := range seen {
		if !reflect.DeepEqual(o[k], n[k]) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "changed: (formatting only)"
	}
	return "changed: " + strings.Join(keys, ",")
}

// configEditPhrase is what the user must type to save config.yaml from the
// browser. The config holds the guardrail `forbid` rules, so rewriting it is
// the most sensitive operation the web UI offers.
const configEditPhrase = "EDIT CONFIG"

// apiConfigPut writes new YAML to disk after strict validation and a typed
// confirmation (server-side --yes cannot skip it). It deliberately does not go
// through gateConfirm: that consults the current policy, and a config.yaml that
// no longer parses must still be repairable from here.
func (s *Server) apiConfigPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML         string `json:"yaml"`
		ConfirmToken string `json:"confirm_token"`
		ConfirmValue string `json:"confirm_value"`
	}
	if err := s.decodeJSON(w, r, &body); err != nil {
		s.render.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := parseConfig([]byte(body.YAML)); err != nil {
		s.render.Error(w, http.StatusBadRequest, "config is not valid: "+err.Error())
		return
	}
	sess := s.readSession(r)
	digest := digestOf("config-edit", body.YAML)
	if body.ConfirmToken == "" {
		bannerText, bannerKind := s.bannerFor(sess)
		tok := s.pty.Issue(ptyConfirmEntry{
			Verb: "config-edit", Context: sess.Context, Resource: "config.yaml",
			Expect: configEditPhrase, Digest: digest,
		})
		s.render.JSON(w, http.StatusOK, confirmationResponse{
			Status: "needs_confirmation", Style: "typed_phrase",
			Prompt: "Type " + configEditPhrase + " to rewrite config.yaml",
			Detail: "This file holds guardrail forbid rules; saving applies to the next operation.",
			Expect: configEditPhrase, Token: tok, TTLSeconds: 30,
			BannerText: bannerText, BannerKind: bannerKind,
		})
		return
	}
	if _, ok := s.pty.Consume(body.ConfirmToken, body.ConfirmValue, "config-edit", sess.Context, digest); !ok {
		s.render.Error(w, http.StatusGone, "confirmation expired or did not match")
		return
	}
	path := config.ConfigFile()
	oldData, _ := os.ReadFile(path)
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(path, []byte(body.YAML), 0o600); err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordWebAudit(r, "config-edit", []string{path, configChangeSummary(oldData, []byte(body.YAML))}, 0, 0)
	s.render.JSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// apiConfigInit writes a commented default. Errors if a file already exists
// unless ?force=true is set (mirrors `sk config init --force`).
func (s *Server) apiConfigInit(w http.ResponseWriter, r *http.Request) {
	path := config.ConfigFile()
	force := r.URL.Query().Get("force") == "1"
	if _, err := os.Stat(path); err == nil {
		if !force {
			s.render.Error(w, http.StatusConflict, "config already exists; pass force=1 to overwrite")
			return
		}
		// Overwriting wipes the guardrail forbid rules: same typed
		// confirmation as a PUT.
		var body struct {
			ConfirmToken string `json:"confirm_token"`
			ConfirmValue string `json:"confirm_value"`
		}
		if r.ContentLength != 0 {
			_ = s.decodeJSON(w, r, &body)
		}
		sess := s.readSession(r)
		digest := digestOf("config-init", path)
		if body.ConfirmToken == "" {
			bannerText, bannerKind := s.bannerFor(sess)
			tok := s.pty.Issue(ptyConfirmEntry{
				Verb: "config-init", Context: sess.Context, Resource: "config.yaml",
				Expect: configEditPhrase, Digest: digest,
			})
			s.render.JSON(w, http.StatusOK, confirmationResponse{
				Status: "needs_confirmation", Style: "typed_phrase",
				Prompt: "Type " + configEditPhrase + " to overwrite config.yaml with the defaults",
				Detail: "This discards every existing forbid rule.",
				Expect: configEditPhrase, Token: tok, TTLSeconds: 30,
				BannerText: bannerText, BannerKind: bannerKind,
			})
			return
		}
		if _, ok := s.pty.Consume(body.ConfirmToken, body.ConfirmValue, "config-init", sess.Context, digest); !ok {
			s.render.Error(w, http.StatusGone, "confirmation expired or did not match")
			return
		}
	}
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(path, []byte(defaultConfigYAML), 0o600); err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordWebAudit(r, "config-init", []string{path}, 0, 0)
	s.render.JSON(w, http.StatusOK, map[string]string{"status": "initialized", "path": path})
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

// defaultConfigYAML is the commented bootstrap shipped by `sk config init`.
// Kept inline rather than embedded so the web and CLI can ship identical
// defaults from one place (the CLI also reads this same string when we wire
// it through; for now, the structure mirrors the documented schema).
const defaultConfigYAML = `# superkube config.yaml
#
# Edits take effect on the next sk invocation; the web UI reloads policy
# automatically on save.

ai:
  # provider: claude        # auto-detect if unset
  # timeout: 90s

audit:
  enabled: true

guardrails:
  # require_typed_confirm: true   # upgrade YesNo to typed phrases globally

contexts:
  # "*prod*":
  #   require_typed_confirm: true
  #   banner: "PRODUCTION"
  #   forbid:
  #     - "delete --all"
  #     - "drain"
`
