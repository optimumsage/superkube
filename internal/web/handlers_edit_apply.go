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
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
}

// parseManifestDocs splits yamlText exactly the way kubectl does (the
// apimachinery YAML/JSON stream decoder, so `--- # comment` and CRLF
// separators behave identically) and returns every non-empty document.
func parseManifestDocs(yamlText string) ([]manifestDoc, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(yamlText), 4096)
	var docs []manifestDoc
	for {
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
		if err := collectManifestDocs(raw, 0, "", "", &docs); err != nil {
			return nil, err
		}
	}
}

const maxListDepth = 8

// collectManifestDocs appends the identity of obj. A `kind: List` / `*List`
// document is unpacked recursively (kubectl applies its items, not the List),
// so forbid rules cannot be dodged by wrapping an object in a List.
//
// A typed list (`kind: SecretList`) may omit kind/apiVersion on its items;
// kubectl infers the item kind from the parent, so inheritKind/inheritAPI carry
// `SecretList` -> `Secret` and the parent's apiVersion down to bare items.
func collectManifestDocs(obj map[string]any, depth int, inheritKind, inheritAPI string, out *[]manifestDoc) error {
	kind, _ := obj["kind"].(string)
	apiVersion, _ := obj["apiVersion"].(string)
	if kind == "" {
		kind = inheritKind
	}
	if apiVersion == "" {
		apiVersion = inheritAPI
	}
	if kind == "List" || strings.HasSuffix(kind, "List") {
		if depth >= maxListDepth {
			return errors.New("invalid YAML: lists nested too deeply")
		}
		items, hasItems := obj["items"]
		if hasItems && items != nil {
			list, ok := items.([]any)
			if !ok {
				return errors.New("invalid YAML: List items must be a list")
			}
			for _, it := range list {
				m, ok := it.(map[string]any)
				if !ok {
					return errors.New("invalid YAML: List item must be an object")
				}
				itemKind := ""
				if kind != "List" {
					itemKind = strings.TrimSuffix(kind, "List")
				}
				if err := collectManifestDocs(m, depth+1, itemKind, apiVersion, out); err != nil {
					return err
				}
			}
			return nil
		}
	}
	md, _ := obj["metadata"].(map[string]any)
	name, _ := md["name"].(string)
	ns, _ := md["namespace"].(string)
	*out = append(*out, manifestDoc{APIVersion: apiVersion, Kind: kind, Name: name, Namespace: ns})
	return nil
}

// validateEditYAML ensures an edit submission is exactly one document whose
// kind, namespace and name are the object named in the URL. Without this the
// edit endpoint (gated as "edit <kind> <name>") would be an arbitrary apply.
// An omitted metadata.namespace is accepted: the edit handlers always pass
// `-n {ns}` from the URL, so kubectl resolves it to nsWant.
func validateEditYAML(kind, nsWant, name, yamlText string) error {
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
		gotNS = nsWant
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
	ConfirmValue string          `json:"confirm_value"`
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
// is an apply under the hood, so `forbid: edit` rules (matched on the URL kind,
// canonical Kind, name, kind/name and -n ns) and every `forbid: apply` rule
// (checked per document, List items included) block it. sess.Namespace must
// already be the edit's URL namespace.
func (s *Server) editForbidden(w http.ResponseWriter, sess session, kind, name, yamlText string) bool {
	canon := canonicalEditKind[kind]
	args := []string{kind, name, strings.ToLower(canon), canon, strings.ToLower(canon) + "/" + name, "-n", sess.Namespace}
	return s.gateForbidden(w, sess, "edit", args) || s.applyForbidden(w, sess, yamlText)
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
	if s.badInput(w, ns, name) {
		return
	}
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
	// The URL namespace is authoritative for an edit (the object may live in
	// a namespace other than the one the header is showing).
	sess.Namespace = ns
	// Forbid-policy: edit shares the same gate as delete/apply — a context
	// flagged forbidden cannot be edited from the web UI either.
	if s.editForbidden(w, sess, kind, name, yamlText) {
		return
	}
	if err := validateEditYAML(kind, ns, name, yamlText); err != nil {
		s.render.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	target := kind + "/" + ns + "/" + name
	s.previewYAML(w, r, sess, "edit", target, yamlDigest(sess, "edit", target, yamlText), yamlText, name)
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
	if s.badInput(w, ns, name) {
		return
	}
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
	sess.Namespace = ns
	if s.editForbidden(w, sess, kind, name, yamlText) {
		return
	}
	target := kind + "/" + ns + "/" + name
	entry, ok := s.consumePreview(w, sess, body.Token, body.ConfirmValue, "edit", yamlDigest(sess, "edit", target, yamlText))
	if !ok {
		return
	}
	// Defense in depth: the digest already proves this is the text that was
	// validated at preview time.
	if err := validateEditYAML(kind, ns, name, yamlText); err != nil {
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
