package web

import (
	"encoding/json"
	"io"
	"net/http"
	"os"

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

// apiConfigPut writes new YAML to disk after validating it parses into our
// Config schema. Invalid YAML is rejected without touching the file.
func (s *Server) apiConfigPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML string `json:"yaml"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.render.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	var probe config.Config
	if err := yaml.Unmarshal([]byte(body.YAML), &probe); err != nil {
		s.render.Error(w, http.StatusBadRequest, "yaml does not parse: "+err.Error())
		return
	}
	path := config.ConfigFile()
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(path, []byte(body.YAML), 0o600); err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordWebAudit("config-put", []string{path}, 0, 0)
	s.render.JSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// apiConfigInit writes a commented default. Errors if a file already exists
// unless ?force=true is set (mirrors `sk config init --force`).
func (s *Server) apiConfigInit(w http.ResponseWriter, r *http.Request) {
	path := config.ConfigFile()
	force := r.URL.Query().Get("force") == "1"
	if _, err := os.Stat(path); err == nil && !force {
		s.render.Error(w, http.StatusConflict, "config already exists; pass force=1 to overwrite")
		return
	}
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(path, []byte(defaultConfigYAML), 0o600); err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordWebAudit("config-init", []string{path}, 0, 0)
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
