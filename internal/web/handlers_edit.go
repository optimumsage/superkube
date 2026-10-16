package web

import (
	"bytes"
	"net/http"
	"time"

	"github.com/optimumsage/superkube/internal/kubectl"
	"github.com/optimumsage/superkube/internal/secretyaml"
)

// editableKinds is the allowlist for the inline edit page. The list/yaml
// endpoints already work for any kubectl-known kind, but the edit handlers
// only accept these. Keeping the set explicit means new types are an opt-in
// decision rather than an accident.
var editableKinds = map[string]bool{
	"configmaps":  true,
	"configmap":   true,
	"cm":          true,
	"secrets":     true,
	"secret":      true,
	"ingresses":   true,
	"ingress":     true,
	"ing":         true,
	"deployments": true,
	"deployment":  true,
	"services":    true,
	"service":     true,
	"svc":         true,
}

// fragResourceEdit renders the inline edit page (textarea + diff preview +
// confirm). The Alpine component fetches the live YAML through the existing
// /api/v1/resources/{kind}/{ns}/{name}/yaml endpoint and uses preview/commit
// to apply.
func (s *Server) fragResourceEdit(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	ns := r.PathValue("ns")
	name := r.PathValue("name")
	if s.badInput(w, ns, name) {
		return
	}
	if !editableKinds[kind] {
		s.render.Error(w, http.StatusNotFound, "edit not supported for kind "+kind)
		return
	}
	_ = s.render.Template(w, "resource_edit.html", map[string]string{
		"Kind":      kind,
		"KindTitle": titleKind(kind),
		"Namespace": ns,
		"Name":      name,
	})
}

// apiSecretYAMLRevealed returns the secret's YAML with `data:` values base64-
// decoded. Routed via apiResourceYAML when `?reveal=1` is set for a secret.
//
// We intentionally don't add additional auth here beyond what the rest of the
// site uses (session cookie + CSRF for mutations; the bind/token model for
// non-loopback access). The web UI's threat model is "trusted local user";
// reveal still gets recorded in the audit log.
func (s *Server) apiSecretYAMLRevealed(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	name := r.PathValue("name")
	if s.badInput(w, ns, name) {
		return
	}
	sess := s.readSession(r)
	full := sess.prependGlobalFlagsNoNS([]string{"get", "secret", name, "-n", ns, "-o", "yaml"})

	var buf bytes.Buffer
	start := time.Now()
	err := s.deps.Runner.Run(r.Context(), full, kubectl.RunOpts{Stdout: &buf, Stderr: &buf})
	exitCode := 0
	if err != nil {
		exitCode = exitCodeOf(err)
	}
	s.recordWebAudit(r, "secret-reveal", []string{ns + "/" + name}, exitCode, time.Since(start))

	if err != nil && exitCode != 0 && buf.Len() == 0 {
		s.render.Error(w, http.StatusBadGateway, err.Error())
		return
	}

	out := []byte(secretyaml.Reveal(buf.String()))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(out)
}
