package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"

	"github.com/optimumsage/superkube/internal/kubectl"
)

// kvPair is the shared shape returned to the form UI for ConfigMap / Secret
// key/value entries.
type kvPair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// configMapForm is the JSON shape served to the ConfigMap form editor and
// accepted back from it. Only the bits the UI exposes are present; the rest
// of the manifest (metadata, labels, annotations, owner refs) is preserved by
// merging into the cluster's current object on save.
type configMapForm struct {
	Name        string   `json:"name"`
	Namespace   string   `json:"namespace"`
	Data        []kvPair `json:"data"`
	BinaryData  []kvPair `json:"binaryData"`
	Labels      []kvPair `json:"labels"`
	Annotations []kvPair `json:"annotations"`
	Immutable   *bool    `json:"immutable,omitempty"`
}

// secretForm mirrors configMapForm but with the secret type and a bool that
// the UI sets when the user has explicitly asked to reveal decoded values.
// Values are always plain text in the JSON; the server base64-encodes them
// when writing back to the cluster.
type secretForm struct {
	Name        string   `json:"name"`
	Namespace   string   `json:"namespace"`
	Type        string   `json:"type"`
	Data        []kvPair `json:"data"`
	Labels      []kvPair `json:"labels"`
	Annotations []kvPair `json:"annotations"`
	Immutable   *bool    `json:"immutable,omitempty"`
}

// ingressForm is the structured shape the Ingress form binds to. Only the
// fields we surface in the UI are listed; everything else round-trips through
// the original YAML.
type ingressForm struct {
	Name             string             `json:"name"`
	Namespace        string             `json:"namespace"`
	IngressClassName string             `json:"ingressClassName"`
	Labels           []kvPair           `json:"labels"`
	Annotations      []kvPair           `json:"annotations"`
	TLS              []ingressTLSForm   `json:"tls"`
	Rules            []ingressRuleForm  `json:"rules"`
	DefaultBackend   *ingressBackendRef `json:"defaultBackend,omitempty"`
}

type ingressTLSForm struct {
	Hosts      []string `json:"hosts"`
	SecretName string   `json:"secretName"`
}

type ingressRuleForm struct {
	Host  string            `json:"host"`
	Paths []ingressPathForm `json:"paths"`
}

type ingressPathForm struct {
	Path     string             `json:"path"`
	PathType string             `json:"pathType"`
	Backend  *ingressBackendRef `json:"backend,omitempty"`
}

type ingressBackendRef struct {
	ServiceName string `json:"serviceName"`
	ServicePort string `json:"servicePort"` // port name or stringified number
}

// apiResourceForm returns the structured form JSON for a single object. The
// client uses it to populate the per-kind form viewer/editor. We fetch the
// live YAML through the same kubectl runner the rest of the server uses, so
// auth/session flags apply identically.
func (s *Server) apiResourceForm(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	ns := r.PathValue("ns")
	name := r.PathValue("name")
	if !editableKinds[kind] {
		s.render.Error(w, http.StatusNotFound, "form not supported for kind "+kind)
		return
	}

	raw, err := s.fetchObjectYAML(r, ns, name, kind)
	if err != nil {
		s.render.Error(w, http.StatusBadGateway, err.Error())
		return
	}

	switch normalizeKind(kind) {
	case "configmap":
		var cm corev1.ConfigMap
		if err := yaml.Unmarshal(raw, &cm); err != nil {
			s.render.Error(w, http.StatusBadGateway, "decode configmap: "+err.Error())
			return
		}
		s.render.JSON(w, http.StatusOK, map[string]any{
			"kind": "configmap",
			"form": configMapToForm(&cm),
			"yaml": string(raw),
		})
	case "secret":
		var sec corev1.Secret
		if err := yaml.Unmarshal(raw, &sec); err != nil {
			s.render.Error(w, http.StatusBadGateway, "decode secret: "+err.Error())
			return
		}
		s.render.JSON(w, http.StatusOK, map[string]any{
			"kind": "secret",
			"form": secretToForm(&sec),
			"yaml": string(raw),
		})
	case "ingress":
		var ing networkingv1.Ingress
		if err := yaml.Unmarshal(raw, &ing); err != nil {
			s.render.Error(w, http.StatusBadGateway, "decode ingress: "+err.Error())
			return
		}
		s.render.JSON(w, http.StatusOK, map[string]any{
			"kind": "ingress",
			"form": ingressToForm(&ing),
			"yaml": string(raw),
		})
	default:
		// Other editable kinds (deployments, services) don't have a form yet —
		// they fall back to the YAML editor.
		s.render.JSON(w, http.StatusOK, map[string]any{
			"kind": normalizeKind(kind),
			"form": nil,
			"yaml": string(raw),
		})
	}
}

// fetchObjectYAML runs `kubectl get <kind> <name> -n <ns> -o yaml` through
// the same session-aware pipeline the YAML viewer uses and returns the raw
// bytes. Errors are returned verbatim; the caller decides how to surface them.
func (s *Server) fetchObjectYAML(r *http.Request, ns, name, kind string) ([]byte, error) {
	sess := s.readSession(r)
	args := sess.prependGlobalFlagsNoNS([]string{"get", kind, name, "-n", ns, "-o", "yaml"})
	var buf bytes.Buffer
	err := s.deps.Runner.Run(r.Context(), args, kubectl.RunOpts{Stdout: &buf, Stderr: &buf})
	if err != nil {
		if buf.Len() > 0 {
			return nil, fmt.Errorf("%s", strings.TrimSpace(buf.String()))
		}
		return nil, err
	}
	return buf.Bytes(), nil
}

// buildYAMLFromForm reads the user's form payload + the original YAML the
// client received, merges the form's edits into a typed struct populated
// from original, and re-marshals to YAML. The merge approach (rather than
// replace) preserves spec fields and metadata the UI didn't expose.
func buildYAMLFromForm(kind string, originalYAML []byte, form json.RawMessage) ([]byte, error) {
	switch normalizeKind(kind) {
	case "configmap":
		var f configMapForm
		if err := json.Unmarshal(form, &f); err != nil {
			return nil, fmt.Errorf("decode configmap form: %w", err)
		}
		var cm corev1.ConfigMap
		if len(originalYAML) > 0 {
			if err := yaml.Unmarshal(originalYAML, &cm); err != nil {
				return nil, fmt.Errorf("re-parse original yaml: %w", err)
			}
		}
		applyConfigMapForm(&cm, &f)
		return marshalCleanYAML(&cm)
	case "secret":
		var f secretForm
		if err := json.Unmarshal(form, &f); err != nil {
			return nil, fmt.Errorf("decode secret form: %w", err)
		}
		var sec corev1.Secret
		if len(originalYAML) > 0 {
			if err := yaml.Unmarshal(originalYAML, &sec); err != nil {
				return nil, fmt.Errorf("re-parse original yaml: %w", err)
			}
		}
		applySecretForm(&sec, &f)
		return marshalCleanYAML(&sec)
	case "ingress":
		var f ingressForm
		if err := json.Unmarshal(form, &f); err != nil {
			return nil, fmt.Errorf("decode ingress form: %w", err)
		}
		var ing networkingv1.Ingress
		if len(originalYAML) > 0 {
			if err := yaml.Unmarshal(originalYAML, &ing); err != nil {
				return nil, fmt.Errorf("re-parse original yaml: %w", err)
			}
		}
		applyIngressForm(&ing, &f)
		return marshalCleanYAML(&ing)
	}
	return nil, fmt.Errorf("form merge not supported for kind %s", kind)
}

// marshalCleanYAML round-trips obj through JSON and back to YAML so the
// output is free of the empty `creationTimestamp: null` / `status: {}` noise
// the default marshaler emits. apiequality and the apply diff are then
// comparing the manifest the user actually wrote.
func marshalCleanYAML(obj runtime.Object) ([]byte, error) {
	jsonBytes, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	// Round-trip via a generic map so we can prune zero-valued plumbing fields
	// that don't belong in a clean manifest.
	var m map[string]any
	if err := json.Unmarshal(jsonBytes, &m); err != nil {
		return nil, err
	}
	pruneEmpty(m)
	if md, ok := m["metadata"].(map[string]any); ok {
		delete(md, "creationTimestamp")
		delete(md, "managedFields")
		delete(md, "resourceVersion")
		delete(md, "selfLink")
		delete(md, "uid")
		delete(md, "generation")
	}
	delete(m, "status")
	return yaml.Marshal(m)
}

// pruneEmpty walks m and removes keys whose value is a nil, "", empty map, or
// empty slice. Keeps booleans and 0 numbers since those are meaningful.
func pruneEmpty(m map[string]any) {
	for k, v := range m {
		switch t := v.(type) {
		case nil:
			delete(m, k)
		case string:
			if t == "" {
				delete(m, k)
			}
		case map[string]any:
			pruneEmpty(t)
			if len(t) == 0 {
				delete(m, k)
			}
		case []any:
			if len(t) == 0 {
				delete(m, k)
			}
		}
	}
}

// --- ConfigMap form ---------------------------------------------------------

func configMapToForm(cm *corev1.ConfigMap) configMapForm {
	return configMapForm{
		Name:        cm.Name,
		Namespace:   cm.Namespace,
		Data:        mapToKVSorted(cm.Data),
		BinaryData:  binaryDataToKV(cm.BinaryData),
		Labels:      mapToKVSorted(cm.Labels),
		Annotations: mapToKVSorted(cm.Annotations),
		Immutable:   cm.Immutable,
	}
}

func applyConfigMapForm(cm *corev1.ConfigMap, f *configMapForm) {
	if cm.APIVersion == "" {
		cm.APIVersion = "v1"
	}
	if cm.Kind == "" {
		cm.Kind = "ConfigMap"
	}
	cm.Name = f.Name
	cm.Namespace = f.Namespace
	cm.Data = kvToMap(f.Data)
	cm.BinaryData = kvToBinaryData(f.BinaryData)
	cm.Labels = kvToMap(f.Labels)
	cm.Annotations = kvToMap(f.Annotations)
	cm.Immutable = f.Immutable
}

// --- Secret form ------------------------------------------------------------

func secretToForm(sec *corev1.Secret) secretForm {
	data := make([]kvPair, 0, len(sec.Data)+len(sec.StringData))
	keys := make([]string, 0, len(sec.Data))
	for k := range sec.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		data = append(data, kvPair{Key: k, Value: string(sec.Data[k])})
	}
	// StringData wins over Data when both are present, mirroring kubectl
	// behavior. UI sees the StringData value as the source of truth.
	for k, v := range sec.StringData {
		replaced := false
		for i := range data {
			if data[i].Key == k {
				data[i].Value = v
				replaced = true
				break
			}
		}
		if !replaced {
			data = append(data, kvPair{Key: k, Value: v})
		}
	}
	t := string(sec.Type)
	if t == "" {
		t = "Opaque"
	}
	return secretForm{
		Name:        sec.Name,
		Namespace:   sec.Namespace,
		Type:        t,
		Data:        data,
		Labels:      mapToKVSorted(sec.Labels),
		Annotations: mapToKVSorted(sec.Annotations),
		Immutable:   sec.Immutable,
	}
}

func applySecretForm(sec *corev1.Secret, f *secretForm) {
	if sec.APIVersion == "" {
		sec.APIVersion = "v1"
	}
	if sec.Kind == "" {
		sec.Kind = "Secret"
	}
	sec.Name = f.Name
	sec.Namespace = f.Namespace
	if f.Type != "" {
		sec.Type = corev1.SecretType(f.Type)
	}
	// We write to Data directly (base64-encoded bytes) so the manifest is
	// self-contained and apply-able as-is. Using stringData would also work,
	// but mixing the two on round-trip gets confusing.
	sec.StringData = nil
	sec.Data = make(map[string][]byte, len(f.Data))
	for _, kv := range f.Data {
		sec.Data[kv.Key] = []byte(kv.Value)
	}
	sec.Labels = kvToMap(f.Labels)
	sec.Annotations = kvToMap(f.Annotations)
	sec.Immutable = f.Immutable
}

// --- Ingress form -----------------------------------------------------------

func ingressToForm(ing *networkingv1.Ingress) ingressForm {
	cls := ""
	if ing.Spec.IngressClassName != nil {
		cls = *ing.Spec.IngressClassName
	}
	tls := make([]ingressTLSForm, 0, len(ing.Spec.TLS))
	for _, t := range ing.Spec.TLS {
		hosts := append([]string{}, t.Hosts...)
		tls = append(tls, ingressTLSForm{Hosts: hosts, SecretName: t.SecretName})
	}
	rules := make([]ingressRuleForm, 0, len(ing.Spec.Rules))
	for _, r := range ing.Spec.Rules {
		rf := ingressRuleForm{Host: r.Host}
		if r.HTTP != nil {
			for _, p := range r.HTTP.Paths {
				rf.Paths = append(rf.Paths, ingressPathForm{
					Path:     p.Path,
					PathType: stringPathType(p.PathType),
					Backend:  backendToForm(&p.Backend),
				})
			}
		}
		rules = append(rules, rf)
	}
	return ingressForm{
		Name:             ing.Name,
		Namespace:        ing.Namespace,
		IngressClassName: cls,
		Labels:           mapToKVSorted(ing.Labels),
		Annotations:      mapToKVSorted(ing.Annotations),
		TLS:              tls,
		Rules:            rules,
		DefaultBackend:   backendToForm(ing.Spec.DefaultBackend),
	}
}

func applyIngressForm(ing *networkingv1.Ingress, f *ingressForm) {
	if ing.APIVersion == "" {
		ing.APIVersion = "networking.k8s.io/v1"
	}
	if ing.Kind == "" {
		ing.Kind = "Ingress"
	}
	ing.Name = f.Name
	ing.Namespace = f.Namespace
	ing.Labels = kvToMap(f.Labels)
	ing.Annotations = kvToMap(f.Annotations)
	if f.IngressClassName != "" {
		v := f.IngressClassName
		ing.Spec.IngressClassName = &v
	} else {
		ing.Spec.IngressClassName = nil
	}
	if len(f.TLS) == 0 {
		ing.Spec.TLS = nil
	} else {
		ing.Spec.TLS = nil
		for _, t := range f.TLS {
			ing.Spec.TLS = append(ing.Spec.TLS, networkingv1.IngressTLS{
				Hosts: append([]string{}, t.Hosts...), SecretName: t.SecretName,
			})
		}
	}
	if len(f.Rules) == 0 {
		ing.Spec.Rules = nil
	} else {
		ing.Spec.Rules = nil
		for _, rf := range f.Rules {
			rule := networkingv1.IngressRule{Host: rf.Host}
			if len(rf.Paths) > 0 {
				rule.HTTP = &networkingv1.HTTPIngressRuleValue{}
				for _, pf := range rf.Paths {
					pt := pathTypeFromString(pf.PathType)
					backend := backendFromForm(pf.Backend)
					rule.HTTP.Paths = append(rule.HTTP.Paths, networkingv1.HTTPIngressPath{
						Path:     pf.Path,
						PathType: &pt,
						Backend:  backend,
					})
				}
			}
			ing.Spec.Rules = append(ing.Spec.Rules, rule)
		}
	}
	if f.DefaultBackend == nil || (f.DefaultBackend.ServiceName == "" && f.DefaultBackend.ServicePort == "") {
		ing.Spec.DefaultBackend = nil
	} else {
		b := backendFromForm(f.DefaultBackend)
		ing.Spec.DefaultBackend = &b
	}
}

func backendToForm(b *networkingv1.IngressBackend) *ingressBackendRef {
	if b == nil || b.Service == nil {
		return nil
	}
	port := ""
	if b.Service.Port.Name != "" {
		port = b.Service.Port.Name
	} else if b.Service.Port.Number != 0 {
		port = fmt.Sprintf("%d", b.Service.Port.Number)
	}
	return &ingressBackendRef{ServiceName: b.Service.Name, ServicePort: port}
}

func backendFromForm(r *ingressBackendRef) networkingv1.IngressBackend {
	if r == nil {
		return networkingv1.IngressBackend{}
	}
	port := networkingv1.ServiceBackendPort{}
	if r.ServicePort != "" {
		if n := parsePortNumber(r.ServicePort); n > 0 {
			port.Number = n
		} else {
			port.Name = r.ServicePort
		}
	}
	return networkingv1.IngressBackend{
		Service: &networkingv1.IngressServiceBackend{
			Name: r.ServiceName,
			Port: port,
		},
	}
}

func parsePortNumber(s string) int32 {
	if s == "" {
		return 0
	}
	var n int32
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int32(r-'0')
		if n > 65535 {
			return 0
		}
	}
	return n
}

func stringPathType(p *networkingv1.PathType) string {
	if p == nil {
		return "ImplementationSpecific"
	}
	return string(*p)
}

func pathTypeFromString(s string) networkingv1.PathType {
	switch s {
	case "Exact", "Prefix", "ImplementationSpecific":
		return networkingv1.PathType(s)
	}
	return networkingv1.PathTypeImplementationSpecific
}

// avoid "unused" lint if intstr isn't otherwise referenced — it's part of the
// implicit import surface of the typed networkingv1 structs we ship as JSON.
var _ = intstr.FromInt

// --- helpers ----------------------------------------------------------------

func mapToKVSorted(m map[string]string) []kvPair {
	if len(m) == 0 {
		return []kvPair{}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]kvPair, 0, len(keys))
	for _, k := range keys {
		out = append(out, kvPair{Key: k, Value: m[k]})
	}
	return out
}

func binaryDataToKV(m map[string][]byte) []kvPair {
	if len(m) == 0 {
		return []kvPair{}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]kvPair, 0, len(keys))
	for _, k := range keys {
		// binaryData stays base64-encoded in the form — binary bytes can't be
		// rendered as text in the UI safely.
		out = append(out, kvPair{Key: k, Value: base64.StdEncoding.EncodeToString(m[k])})
	}
	return out
}

func kvToMap(kvs []kvPair) map[string]string {
	if len(kvs) == 0 {
		return nil
	}
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		if kv.Key == "" {
			continue
		}
		out[kv.Key] = kv.Value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func kvToBinaryData(kvs []kvPair) map[string][]byte {
	if len(kvs) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(kvs))
	for _, kv := range kvs {
		if kv.Key == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(kv.Value)
		if err != nil {
			// Keep the bytes as-is if base64 is malformed; the apply will fail
			// loudly, which is better than silently corrupting the value.
			out[kv.Key] = []byte(kv.Value)
			continue
		}
		out[kv.Key] = raw
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeKind collapses the kubectl-flavored spellings (cm, configmap,
// configmaps, ing, ingresses, …) into a single canonical name used by the
// dispatch tables in this file.
func normalizeKind(k string) string {
	switch k {
	case "cm", "configmap", "configmaps":
		return "configmap"
	case "secret", "secrets":
		return "secret"
	case "ing", "ingress", "ingresses":
		return "ingress"
	case "deploy", "deployment", "deployments":
		return "deployment"
	case "svc", "service", "services":
		return "service"
	}
	return k
}
