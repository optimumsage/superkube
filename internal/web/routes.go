package web

import (
	"io/fs"
	"net/http"
)

// registerRoutes wires every URL the web UI uses. The grouping below mirrors
// the docs in the plan file; each line should map cleanly to a `sk` verb.
func (s *Server) registerRoutes() {
	mux := s.mux

	// Static assets — served straight from the embedded FS. Index page sets
	// the CSRF cookie before any HTMX call runs.
	staticSub, _ := readEmbedFS()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(mustSub(staticSub, ".")))))
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)

	// Pages — every browser-facing URL ends up at handleIndex; the SPA shell
	// inspects window.location and asks for the right fragment via HTMX. We
	// keep server-side templates for content fragments (data is rendered with
	// html/template) and let HTMX swap them into the shell.
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /pods", s.handleIndex)
	mux.HandleFunc("GET /pods/{ns}/{name}", s.handleIndex)
	mux.HandleFunc("GET /resources/{kind}", s.handleIndex)
	mux.HandleFunc("GET /resources/{kind}/{ns}/{name}/edit", s.handleIndex)
	mux.HandleFunc("GET /apply", s.handleIndex)
	mux.HandleFunc("GET /logs", s.handleIndex)
	mux.HandleFunc("GET /logs/multi", s.handleIndex)
	mux.HandleFunc("GET /ai", s.handleIndex)
	mux.HandleFunc("GET /pf", s.handleIndex)
	mux.HandleFunc("GET /audit", s.handleIndex)
	mux.HandleFunc("GET /config", s.handleIndex)
	mux.HandleFunc("GET /settings", s.handleIndex)
	mux.HandleFunc("GET /exec/{ns}/{name}", s.handleIndex)
	mux.HandleFunc("GET /helm", s.handleIndex)
	mux.HandleFunc("GET /helm/install", s.handleIndex)
	mux.HandleFunc("GET /helm/repos", s.handleIndex)
	mux.HandleFunc("GET /helm/{ns}/{name}", s.handleIndex)

	// Fragment endpoints — return HTML fragments designed to be swapped into
	// #main by HTMX. Keep them under /frag/ so they're easy to spot in logs.
	mux.HandleFunc("GET /frag/dashboard", s.fragDashboard)
	mux.HandleFunc("GET /frag/pods", s.fragPods)
	mux.HandleFunc("GET /frag/pod/{ns}/{name}", s.fragPodDetail)
	mux.HandleFunc("GET /frag/resources/{kind}", s.fragResources)
	mux.HandleFunc("GET /frag/resources/{kind}/{ns}/{name}/edit", s.fragResourceEdit)
	mux.HandleFunc("GET /frag/apply", s.fragApply)
	mux.HandleFunc("GET /frag/logs", s.fragLogs)
	mux.HandleFunc("GET /frag/logs-multi", s.fragLogsMulti)
	mux.HandleFunc("GET /frag/ai", s.fragAI)
	mux.HandleFunc("GET /frag/pf", s.fragPF)
	mux.HandleFunc("GET /frag/audit", s.fragAudit)
	mux.HandleFunc("GET /frag/config", s.fragConfigPage)
	mux.HandleFunc("GET /frag/settings", s.fragSettings)
	mux.HandleFunc("GET /frag/exec/{ns}/{name}", s.fragExec)
	mux.HandleFunc("GET /frag/helm", s.fragHelm)
	mux.HandleFunc("GET /frag/helm/install", s.fragHelmInstall)
	mux.HandleFunc("GET /frag/helm/repos", s.fragHelmRepos)
	mux.HandleFunc("GET /frag/helm/{ns}/{name}", s.fragHelmRelease)

	// API v1 — JSON endpoints used by HTMX (out-of-band) and the exec page.
	mux.HandleFunc("GET /api/v1/info", s.apiInfo)
	mux.HandleFunc("GET /api/v1/contexts", s.apiContextsList)
	mux.HandleFunc("POST /api/v1/contexts/switch", s.apiContextSwitch)
	mux.HandleFunc("GET /api/v1/namespaces", s.apiNamespacesList)
	mux.HandleFunc("POST /api/v1/namespaces/switch", s.apiNamespaceSwitch)

	mux.HandleFunc("GET /api/v1/resources/{kind}", s.apiResourceList)
	mux.HandleFunc("GET /api/v1/resources/{kind}/{ns}/{name}/describe", s.apiResourceDescribe)
	mux.HandleFunc("GET /api/v1/resources/{kind}/{ns}/{name}/yaml", s.apiResourceYAML)
	mux.HandleFunc("GET /api/v1/resources/{kind}/{ns}/{name}/events", s.apiResourceEvents)
	mux.HandleFunc("GET /api/v1/resources/{kind}/{ns}/{name}/form", s.apiResourceForm)

	mux.HandleFunc("POST /api/v1/apply/preview", s.apiApplyPreview)
	mux.HandleFunc("POST /api/v1/apply/commit", s.apiApplyCommit)

	mux.HandleFunc("POST /api/v1/resources/{kind}/{ns}/{name}/edit/preview", s.apiResourceEditPreview)
	mux.HandleFunc("POST /api/v1/resources/{kind}/{ns}/{name}/edit/commit", s.apiResourceEditCommit)

	mux.HandleFunc("POST /api/v1/destructive/delete", s.apiDelete)
	mux.HandleFunc("POST /api/v1/destructive/scale", s.apiScale)
	mux.HandleFunc("POST /api/v1/destructive/rollout/{action}", s.apiRollout)
	mux.HandleFunc("POST /api/v1/destructive/drain", s.apiDrain)
	mux.HandleFunc("POST /api/v1/destructive/cordon", s.apiCordon)
	mux.HandleFunc("POST /api/v1/destructive/uncordon", s.apiUncordon)

	mux.HandleFunc("POST /api/v1/ai/explain", s.apiAIExplain)
	mux.HandleFunc("POST /api/v1/ai/diagnose", s.apiAIDiagnose)
	mux.HandleFunc("POST /api/v1/ai/why", s.apiAIWhy)
	mux.HandleFunc("POST /api/v1/ai/logs", s.apiAILogs)

	mux.HandleFunc("GET /api/v1/portforward", s.apiPFList)
	mux.HandleFunc("POST /api/v1/portforward", s.apiPFStart)
	mux.HandleFunc("DELETE /api/v1/portforward/{id}", s.apiPFStop)

	mux.HandleFunc("GET /api/v1/audit", s.apiAuditList)
	mux.HandleFunc("GET /api/v1/audit/stats", s.apiAuditStats)
	mux.HandleFunc("GET /api/v1/audit/path", s.apiAuditPath)

	mux.HandleFunc("GET /api/v1/config", s.apiConfigGet)
	mux.HandleFunc("PUT /api/v1/config", s.apiConfigPut)
	mux.HandleFunc("POST /api/v1/config/init", s.apiConfigInit)

	mux.HandleFunc("POST /api/v1/passthrough", s.apiPassthrough)
	mux.HandleFunc("GET /api/v1/upgrade/check", s.apiUpgradeCheck)

	// Helm — release lifecycle + repo management. Status works without the
	// helm binary; everything else 503s with {installed:false} when missing.
	mux.HandleFunc("GET /api/v1/helm/status", s.apiHelmStatus)
	mux.HandleFunc("GET /api/v1/helm/releases", s.apiHelmReleasesList)
	mux.HandleFunc("GET /api/v1/helm/releases/{ns}/{name}", s.apiHelmReleaseStatus)
	mux.HandleFunc("GET /api/v1/helm/releases/{ns}/{name}/values", s.apiHelmReleaseValues)
	mux.HandleFunc("GET /api/v1/helm/releases/{ns}/{name}/manifest", s.apiHelmReleaseManifest)
	mux.HandleFunc("GET /api/v1/helm/releases/{ns}/{name}/notes", s.apiHelmReleaseNotes)
	mux.HandleFunc("GET /api/v1/helm/releases/{ns}/{name}/hooks", s.apiHelmReleaseHooks)
	mux.HandleFunc("GET /api/v1/helm/releases/{ns}/{name}/history", s.apiHelmReleaseHistory)
	mux.HandleFunc("POST /api/v1/helm/rollback", s.apiHelmRollback)
	mux.HandleFunc("POST /api/v1/helm/uninstall", s.apiHelmUninstall)
	mux.HandleFunc("POST /api/v1/helm/upgrade/preview", s.apiHelmUpgradePreview)
	mux.HandleFunc("POST /api/v1/helm/upgrade/commit", s.apiHelmUpgradeCommit)
	mux.HandleFunc("POST /api/v1/helm/install/preview", s.apiHelmInstallPreview)
	mux.HandleFunc("POST /api/v1/helm/install/commit", s.apiHelmInstallCommit)
	mux.HandleFunc("GET /api/v1/helm/repos", s.apiHelmReposList)
	mux.HandleFunc("POST /api/v1/helm/repos", s.apiHelmRepoAdd)
	mux.HandleFunc("DELETE /api/v1/helm/repos/{name}", s.apiHelmRepoRemove)
	mux.HandleFunc("POST /api/v1/helm/repos/update", s.apiHelmRepoUpdate)
	mux.HandleFunc("GET /api/v1/helm/search", s.apiHelmSearch)
	mux.HandleFunc("GET /api/v1/helm/charts/values", s.apiHelmChartValues)

	// SSE streams.
	mux.HandleFunc("GET /api/v1/stream/watch/{kind}", s.streamWatch)
	mux.HandleFunc("GET /api/v1/stream/logs/{ns}/{pod}", s.streamLogs)
	mux.HandleFunc("GET /api/v1/stream/logs-multi", s.streamLogsMulti)
	mux.HandleFunc("GET /api/v1/stream/portforward/{id}", s.streamPFLogs)
	mux.HandleFunc("GET /api/v1/stream/audit", s.streamAudit)

	// WebSocket — exec into pod.
	mux.HandleFunc("GET /ws/exec/{ns}/{pod}", s.wsExec)
}

// mustSub is a convenience around fs.Sub that just panics on the error — the
// embedded FS is fixed at compile time, so the only path that fails is a bug.
func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
