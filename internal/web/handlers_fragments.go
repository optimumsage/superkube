package web

import (
	"net/http"
	"strings"
)

// Fragment handlers return HTML strings that HTMX swaps into #main. Each one
// renders a template that hosts its own Alpine component. Real data fetching
// happens client-side via the /api/v1 endpoints — keeps the server stateless.

func (s *Server) fragDashboard(w http.ResponseWriter, _ *http.Request) {
	_ = s.render.Template(w, "dashboard.html", nil)
}

func (s *Server) fragPods(w http.ResponseWriter, _ *http.Request) {
	_ = s.render.Template(w, "pods.html", nil)
}

func (s *Server) fragPodDetail(w http.ResponseWriter, r *http.Request) {
	_ = s.render.Template(w, "pod_detail.html", map[string]string{
		"Namespace": r.PathValue("ns"),
		"Name":      r.PathValue("name"),
	})
}

func (s *Server) fragResources(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	_ = s.render.Template(w, "resources.html", map[string]string{
		"Kind":      kind,
		"KindTitle": titleKind(kind),
	})
}

func (s *Server) fragApply(w http.ResponseWriter, _ *http.Request) {
	_ = s.render.Template(w, "apply.html", nil)
}

func (s *Server) fragLogs(w http.ResponseWriter, _ *http.Request) {
	// Single-pod logs are served via the pod detail page's Logs tab; if a user
	// lands on /logs directly we redirect to the multi-pod page.
	_ = s.render.Template(w, "logs_multi.html", nil)
}

func (s *Server) fragLogsMulti(w http.ResponseWriter, _ *http.Request) {
	_ = s.render.Template(w, "logs_multi.html", nil)
}

func (s *Server) fragAI(w http.ResponseWriter, _ *http.Request) {
	_ = s.render.Template(w, "ai.html", nil)
}

func (s *Server) fragPF(w http.ResponseWriter, _ *http.Request) {
	_ = s.render.Template(w, "portforward.html", nil)
}

func (s *Server) fragAudit(w http.ResponseWriter, _ *http.Request) {
	_ = s.render.Template(w, "audit.html", nil)
}

func (s *Server) fragConfigPage(w http.ResponseWriter, _ *http.Request) {
	_ = s.render.Template(w, "config.html", nil)
}

func (s *Server) fragSettings(w http.ResponseWriter, _ *http.Request) {
	_ = s.render.Template(w, "settings.html", nil)
}

func (s *Server) fragExec(w http.ResponseWriter, r *http.Request) {
	_ = s.render.Template(w, "exec.html", map[string]string{
		"Namespace": r.PathValue("ns"),
		"Name":      r.PathValue("name"),
		"Container": r.URL.Query().Get("container"),
	})
}

// titleKind renders a kubectl-style resource name as a Title Case heading
// (e.g. "deployments" → "Deployments", "po" → "Pods").
func titleKind(k string) string {
	switch k {
	case "po":
		return "Pods"
	case "deploy", "deployments":
		return "Deployments"
	case "svc", "services":
		return "Services"
	case "ing", "ingresses":
		return "Ingresses"
	case "no", "nodes":
		return "Nodes"
	case "ns", "namespaces":
		return "Namespaces"
	}
	if len(k) == 0 {
		return ""
	}
	return strings.ToUpper(k[:1]) + k[1:]
}
