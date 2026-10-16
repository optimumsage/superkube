package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// canonicalEditKind maps the URL kind (plural/singular/short name) onto the
// manifest `kind:` the object must carry.
var canonicalEditKind = map[string]string{
	"configmaps": "ConfigMap", "configmap": "ConfigMap", "cm": "ConfigMap",
	"secrets": "Secret", "secret": "Secret",
	"ingresses": "Ingress", "ingress": "Ingress", "ing": "Ingress",
	"deployments": "Deployment", "deployment": "Deployment",
	"services": "Service", "service": "Service", "svc": "Service",
}

// manifestDoc is the identity of one YAML document in a submission.
type manifestDoc struct {
	Kind      string
	Name      string
	Namespace string
}

// parseManifestDocs splits yamlText exactly the way kubectl does (the
// apimachinery YAML/JSON stream decoder, so `--- # comment` and CRLF
// separators behave identically) and returns every non-empty document.
func parseManifestDocs(yamlText string) ([]manifestDoc, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(yamlText), 4096)
	var docs []manifestDoc
	for {
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		var raw map[string]any
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return docs, nil
			}
			return nil, fmt.Errorf("invalid YAML: %w", err)
		}
		if len(raw) == 0 {
			continue // empty / comment-only document
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid YAML: %w", err)
		}
		if err := json.Unmarshal(b, &obj); err != nil {
			return nil, fmt.Errorf("invalid YAML: %w", err)
		}
		docs = append(docs, manifestDoc{Kind: obj.Kind, Name: obj.Metadata.Name, Namespace: obj.Metadata.Namespace})
	}
}

// validateEditYAML ensures an edit submission is exactly one document whose
// kind, namespace and name are the object named in the URL. Without this the
// edit endpoint (gated as "edit <kind> <name>") would be an arbitrary apply.
// An omitted metadata.namespace is accepted only when kubectl would resolve it
// to nsWant anyway (the session namespace).
func validateEditYAML(kind, nsWant, name, sessNS, yamlText string) error {
	wantKind, ok := canonicalEditKind[kind]
	if !ok {
		return fmt.Errorf("edit not supported for kind %s", kind)
	}
	docs, err := parseManifestDocs(yamlText)
	if err != nil {
		return err
	}
	if len(docs) != 1 {
		return errors.New("edit accepts exactly one YAML document")
	}
	obj := docs[0]
	if obj.Kind != wantKind {
		return fmt.Errorf("manifest kind %q does not match %s", obj.Kind, wantKind)
	}
	if obj.Name != name {
		return fmt.Errorf("manifest name %q does not match %q", obj.Name, name)
	}
	gotNS := obj.Namespace
	if gotNS == "" {
		gotNS = sessNS
	}
	if gotNS != nsWant {
		return fmt.Errorf("manifest namespace %q does not match %q", gotNS, nsWant)
	}
	return nil
}

// editBody is the shared request shape for edit preview and commit.
type editBody struct {
	YAML         string          `json:"yaml"`
	Form         json.RawMessage `json:"form"`
	OriginalYAML string          `json:"original_yaml"`
	Token        string          `json:"confirm_token"`
}

// editYAML resolves the manifest text from either text mode (`yaml`) or form
// mode (`form` + `original_yaml`). It writes the error response and returns
// ok=false on failure.
func (s *Server) editYAML(w http.ResponseWriter, kind string, body editBody) (string, bool) {
	yamlText := body.YAML
	if len(body.Form) > 0 && string(body.Form) != "null" {
		built, err := buildYAMLFromForm(kind, []byte(body.OriginalYAML), body.Form)
		if err != nil {
			s.render.Error(w, http.StatusBadRequest, err.Error())
			return "", false
		}
		yamlText = string(built)
	}
	if strings.TrimSpace(yamlText) == "" {
		s.render.Error(w, http.StatusBadRequest, "yaml or form required")
		return "", false
	}
	return yamlText, true
}

// editForbidden runs the forbid gates shared by edit preview and commit. An edit
// is an apply under the hood, so both `forbid: edit` and `forbid: apply`
// rules block it.
func (s *Server) editForbidden(w http.ResponseWriter, sess session, kind, name string) bool {
	return s.gateForbidden(w, sess, "edit", []string{kind, name}) ||
		s.gateForbidden(w, sess, "apply", applyArgsForPolicy)
}

// apiResourceEditPreview computes the diff between the submitted YAML and the
// live object using kubectl diff, then (if there are changes) returns a
// single-use confirmation token bound to the exact YAML and target. Mirrors
// apiApplyPreview.
//
// The body accepts either `yaml` (textual editor mode) or `form` +
// `original_yaml` (form editor mode). When `form` is set we merge it into the
// original-yaml-derived typed object and re-marshal before diffing — this
// keeps spec/status fields the UI doesn't expose intact.
func (s *Server) apiResourceEditPreview(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	ns := r.PathValue("ns")
	name := r.PathValue("name")
	if !editableKinds[kind] {
		s.render.Error(w, http.StatusNotFound, "edit not supported for kind "+kind)
		return
	}
	var body editBody
	if err := s.decodeJSON(w, r, &body); err != nil {
		s.render.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	yamlText, ok := s.editYAML(w, kind, body)
	if !ok {
		return
	}
	sess := s.readSession(r)
	// Forbid-policy: edit shares the same gate as delete/apply — a context
	// flagged forbidden cannot be edited from the web UI either.
	if s.editForbidden(w, sess, kind, name) {
		return
	}
	if err := validateEditYAML(kind, ns, name, sess.Namespace, yamlText); err != nil {
		s.render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	target := kind + "/" + ns + "/" + name
	s.previewYAML(w, r, sess, "edit", target, yamlDigest(sess, "edit", target, yamlText), yamlText)
}

// apiResourceEditCommit consumes the token from apiResourceEditPreview and
// applies exactly the previewed YAML. The rebuilt manifest must hash to the
// digest recorded at preview time, must still describe {kind}/{ns}/{name},
// and the forbid gates run again.
//
// Accepts the same two body shapes as preview: `{yaml,confirm_token}` (text
// mode) or `{form,original_yaml,confirm_token}` (form mode).
func (s *Server) apiResourceEditCommit(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	ns := r.PathValue("ns")
	name := r.PathValue("name")
	if !editableKinds[kind] {
		s.render.Error(w, http.StatusNotFound, "edit not supported for kind "+kind)
		return
	}
	var body editBody
	if err := s.decodeJSON(w, r, &body); err != nil {
		s.render.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	sess := s.readSession(r)
	if s.editForbidden(w, sess, kind, name) {
		return
	}
	yamlText, ok := s.editYAML(w, kind, body)
	if !ok {
		return
	}
	target := kind + "/" + ns + "/" + name
	entry, ok := s.consumePreview(w, sess, body.Token, "", "edit", yamlDigest(sess, "edit", target, yamlText))
	if !ok {
		return
	}
	// Defense in depth: the digest already proves this is the text that was
	// validated at preview time.
	if err := validateEditYAML(kind, ns, name, sess.Namespace, yamlText); err != nil {
		s.render.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	start := time.Now()
	out, exitCode, err := s.applyYAML(r, sess, entry.Payload)
	if err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordWebAudit(r, "edit", []string{kind, ns + "/" + name}, exitCode, time.Since(start))

	if exitCode != 0 {
		s.render.JSON(w, http.StatusBadGateway, map[string]any{
			"status": "error",
			"output": out,
		})
		return
	}
	s.render.JSON(w, http.StatusOK, map[string]any{
		"status": "applied",
		"output": out,
	})
}
