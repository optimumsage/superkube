package web

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func TestConfigMapFormRoundTrip(t *testing.T) {
	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "flags", Namespace: "default", Labels: map[string]string{"app": "web"}},
		Data: map[string]string{
			"FOO":      "bar",
			"MULTI":    "line1\nline2",
			"EMPTYKEY": "",
		},
	}
	f := configMapToForm(cm)
	if f.Name != "flags" || f.Namespace != "default" {
		t.Errorf("identity fields wrong: %+v", f)
	}
	keys := map[string]string{}
	for _, kv := range f.Data {
		keys[kv.Key] = kv.Value
	}
	if keys["FOO"] != "bar" || keys["MULTI"] != "line1\nline2" || keys["EMPTYKEY"] != "" {
		t.Errorf("data round-trip wrong: %+v", keys)
	}
	if len(f.Labels) != 1 || f.Labels[0].Key != "app" || f.Labels[0].Value != "web" {
		t.Errorf("labels wrong: %+v", f.Labels)
	}
}

func TestConfigMapApplyForm_MutatesData(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "ns"},
		Data:       map[string]string{"DROP": "x", "KEEP": "y"},
	}
	form := &configMapForm{
		Name:      "flags",
		Namespace: "ns",
		Data:      []kvPair{{Key: "KEEP", Value: "y"}, {Key: "NEW", Value: "z"}},
	}
	applyConfigMapForm(cm, form)
	if cm.Name != "flags" {
		t.Errorf("name not updated: %s", cm.Name)
	}
	if _, exists := cm.Data["DROP"]; exists {
		t.Errorf("DROP key should have been removed: %+v", cm.Data)
	}
	if cm.Data["NEW"] != "z" || cm.Data["KEEP"] != "y" {
		t.Errorf("data wrong: %+v", cm.Data)
	}
}

func TestSecretFormRoundTrip_ValuesAreDecoded(t *testing.T) {
	sec := &corev1.Secret{
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"username": []byte("mohsin"),
			"password": []byte("supersecret"),
		},
	}
	sec.Name = "creds"
	sec.Namespace = "default"
	f := secretToForm(sec)
	if f.Type != "Opaque" {
		t.Errorf("type wrong: %q", f.Type)
	}
	values := map[string]string{}
	for _, kv := range f.Data {
		values[kv.Key] = kv.Value
	}
	if values["username"] != "mohsin" || values["password"] != "supersecret" {
		t.Errorf("data values weren't decoded: %+v", values)
	}
}

func TestSecretApplyForm_EncodesToBase64InYAML(t *testing.T) {
	form := &secretForm{
		Name:      "creds",
		Namespace: "default",
		Type:      "Opaque",
		Data:      []kvPair{{Key: "k", Value: "hello world"}},
	}
	var sec corev1.Secret
	applySecretForm(&sec, form)
	got, ok := sec.Data["k"]
	if !ok || string(got) != "hello world" {
		t.Errorf("sec.Data[k] = %q (raw bytes); want plain bytes (yaml marshal handles base64)", got)
	}
	// Marshal to YAML and confirm we get a base64 encoding back out.
	out, err := yaml.Marshal(&sec)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	if !strings.Contains(string(out), "data:") {
		t.Errorf("expected data block: %s", out)
	}
	wantB64 := base64.StdEncoding.EncodeToString([]byte("hello world"))
	if !strings.Contains(string(out), wantB64) {
		t.Errorf("expected base64 of value (%s) in:\n%s", wantB64, out)
	}
}

func TestIngressFormRoundTrip(t *testing.T) {
	cls := "nginx"
	pt := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &cls,
			TLS: []networkingv1.IngressTLS{
				{Hosts: []string{"example.com"}, SecretName: "tls-secret"},
			},
			Rules: []networkingv1.IngressRule{
				{
					Host: "example.com",
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pt,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: "web-svc",
											Port: networkingv1.ServiceBackendPort{Number: 80},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	f := ingressToForm(ing)
	if f.IngressClassName != "nginx" {
		t.Errorf("class wrong: %q", f.IngressClassName)
	}
	if len(f.TLS) != 1 || f.TLS[0].SecretName != "tls-secret" || f.TLS[0].Hosts[0] != "example.com" {
		t.Errorf("tls wrong: %+v", f.TLS)
	}
	if len(f.Rules) != 1 || f.Rules[0].Host != "example.com" {
		t.Errorf("rule host wrong: %+v", f.Rules)
	}
	if len(f.Rules[0].Paths) != 1 {
		t.Fatalf("expected one path, got %d", len(f.Rules[0].Paths))
	}
	p := f.Rules[0].Paths[0]
	if p.Path != "/" || p.PathType != "Prefix" || p.Backend.ServiceName != "web-svc" || p.Backend.ServicePort != "80" {
		t.Errorf("path wrong: %+v", p)
	}
}

func TestIngressApplyForm_AppliesNewRules(t *testing.T) {
	form := &ingressForm{
		Name:             "web",
		Namespace:        "default",
		IngressClassName: "nginx",
		TLS:              []ingressTLSForm{{Hosts: []string{"a.example.com"}, SecretName: "tls"}},
		Rules: []ingressRuleForm{
			{
				Host: "a.example.com",
				Paths: []ingressPathForm{
					{Path: "/api", PathType: "Prefix", Backend: &ingressBackendRef{ServiceName: "api", ServicePort: "8080"}},
				},
			},
		},
	}
	var ing networkingv1.Ingress
	applyIngressForm(&ing, form)
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "nginx" {
		t.Errorf("class not set")
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != "a.example.com" {
		t.Errorf("rule not applied: %+v", ing.Spec.Rules)
	}
	p := ing.Spec.Rules[0].HTTP.Paths[0]
	if p.Path != "/api" || *p.PathType != networkingv1.PathTypePrefix {
		t.Errorf("path wrong: %+v", p)
	}
	if p.Backend.Service.Name != "api" || p.Backend.Service.Port.Number != 8080 {
		t.Errorf("backend wrong: %+v", p.Backend)
	}
}

func TestIngressApplyForm_ClearsRulesWhenFormEmpty(t *testing.T) {
	pt := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{
				Host: "x", IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &pt}},
					},
				},
			}},
		},
	}
	form := &ingressForm{Name: "web", Namespace: "n"}
	applyIngressForm(ing, form)
	if len(ing.Spec.Rules) != 0 {
		t.Errorf("expected rules cleared, got %+v", ing.Spec.Rules)
	}
	if ing.Spec.IngressClassName != nil {
		t.Errorf("expected class cleared")
	}
}

func TestBuildYAMLFromForm_ConfigMap(t *testing.T) {
	original := `apiVersion: v1
kind: ConfigMap
metadata:
  name: flags
  namespace: default
data:
  OLD: "1"
`
	form := json.RawMessage(`{"name":"flags","namespace":"default","data":[{"key":"NEW","value":"2"}]}`)
	got, err := buildYAMLFromForm("configmap", []byte(original), form)
	if err != nil {
		t.Fatalf("buildYAMLFromForm: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, "NEW:") {
		t.Errorf("new key missing in output:\n%s", s)
	}
	if strings.Contains(s, "OLD:") {
		t.Errorf("old key should have been removed:\n%s", s)
	}
	if !strings.Contains(s, "name: flags") {
		t.Errorf("metadata.name missing:\n%s", s)
	}
}

func TestBuildYAMLFromForm_Secret_EncodesValue(t *testing.T) {
	original := `apiVersion: v1
kind: Secret
metadata:
  name: creds
type: Opaque
data:
  username: bW9oc2lu
`
	form := json.RawMessage(`{"name":"creds","namespace":"default","type":"Opaque","data":[{"key":"username","value":"mohsin"},{"key":"password","value":"sk"}]}`)
	got, err := buildYAMLFromForm("secret", []byte(original), form)
	if err != nil {
		t.Fatalf("buildYAMLFromForm: %v", err)
	}
	s := string(got)
	for _, want := range []string{
		base64.StdEncoding.EncodeToString([]byte("mohsin")),
		base64.StdEncoding.EncodeToString([]byte("sk")),
		"type: Opaque",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q:\n%s", want, s)
		}
	}
}

func TestBuildYAMLFromForm_UnsupportedKind(t *testing.T) {
	_, err := buildYAMLFromForm("pods", nil, json.RawMessage(`{}`))
	if err == nil {
		t.Errorf("expected error for unsupported kind")
	}
}

func TestNormalizeKind(t *testing.T) {
	cases := map[string]string{
		"cm":          "configmap",
		"configmap":   "configmap",
		"configmaps":  "configmap",
		"secret":      "secret",
		"secrets":     "secret",
		"ing":         "ingress",
		"ingress":     "ingress",
		"ingresses":   "ingress",
		"deployments": "deployment",
		"services":    "service",
		"unknown":     "unknown",
	}
	for in, want := range cases {
		if got := normalizeKind(in); got != want {
			t.Errorf("normalizeKind(%q) = %q, want %q", in, got, want)
		}
	}
}
