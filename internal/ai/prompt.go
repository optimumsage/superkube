package ai

import (
	"bytes"
	"text/template"
)

// PromptInputs is the variable bundle passed to every prompt template. Fields
// are optional; templates handle missing data gracefully.
type PromptInputs struct {
	Question    string // free-form user prompt (sk ai explain)
	Context     string // current kubeconfig context
	Namespace   string // current namespace
	Resource    string // e.g. "pod/foo", "deployment/bar"
	Describe    string // `kubectl describe` output
	Events      string // recent events for the resource
	Logs        string // last N log lines per container, redacted + truncated
	OwnerChain  string // pod -> rs -> deploy, or empty
	SiblingPods string // summary of other pods in the same workload

	// ToolsAllowed signals to the template that the AI provider will have
	// read-only kubectl/sk Bash access for this run. Templates use this to
	// adjust the framing (don't apologize for missing tools when tools are
	// in fact available).
	ToolsAllowed bool
}

// Render fills the named template with inputs. The input is redacted before
// templating so the redacted form reaches both the template body AND any
// downstream copy of the prompt we might log for `-v`.
func Render(name string, inputs PromptInputs) (string, error) {
	inputs.Describe = Redact(inputs.Describe)
	inputs.Events = Redact(inputs.Events)
	inputs.Logs = Redact(inputs.Logs)
	inputs.SiblingPods = Redact(inputs.SiblingPods)
	tmpl, ok := templates[name]
	if !ok {
		return "", &templateNotFoundError{name: name}
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, inputs); err != nil {
		return "", err
	}
	return b.String(), nil
}

type templateNotFoundError struct{ name string }

func (e *templateNotFoundError) Error() string { return "ai: unknown prompt template " + e.name }

var templates = map[string]*template.Template{
	"explain":  template.Must(template.New("explain").Parse(tmplExplain)),
	"diagnose": template.Must(template.New("diagnose").Parse(tmplDiagnose)),
	"why":      template.Must(template.New("why").Parse(tmplWhy)),
	"logs":     template.Must(template.New("logs").Parse(tmplLogs)),
}

const preamble = `You are assisting a Kubernetes operator running superkube, a kubectl wrapper.
Be concise. Cite specific evidence from the data provided. Don't speculate about
cluster state not shown. If the user's question cannot be answered from the
provided data, say so plainly.

Current kubectl context: {{ if .Context }}{{ .Context }}{{ else }}(default){{ end }}
Current namespace: {{ if .Namespace }}{{ .Namespace }}{{ else }}default{{ end }}
`

const tmplExplain = preamble + `
{{ if .ToolsAllowed -}}
You have permission to run read-only kubectl/sk commands via the Bash tool
(get, describe, logs, events, top, explain, api-resources, api-versions,
version, cluster-info, auth can-i, config view/get-contexts/current-context).
You MUST NOT run any command that mutates state (apply, create, delete, edit,
patch, replace, scale, rollout, cordon, drain, exec, port-forward, debug,
attach). Prefer the smallest set of commands needed to answer. Always pass
-n/--namespace explicitly when querying namespaced resources.
{{- else -}}
You have NO tools available in this session — you cannot run kubectl, sk, or
any shell command. Do not apologize or say you lack permission. Answer the
user from general Kubernetes knowledge, or if the question requires live
cluster state you cannot see, tell the user the exact ` + "`sk`" + ` or
` + "`kubectl`" + ` command they should run themselves (one line, copy-pastable).
{{- end }}

User question:
{{ .Question }}
`

const tmplDiagnose = preamble + `
Diagnose the following Kubernetes workload. Provide:
  1. A one-line summary of the current state.
  2. The most likely root cause, with the specific event/log line(s) as evidence.
  3. Concrete next steps (kubectl commands or manifest changes).

Resource: {{ .Resource }}

--- describe ---
{{ .Describe }}

{{- if .OwnerChain }}
--- owner chain ---
{{ .OwnerChain }}
{{- end }}

{{- if .Events }}
--- recent events ---
{{ .Events }}
{{- end }}

{{- if .SiblingPods }}
--- sibling pods ---
{{ .SiblingPods }}
{{- end }}

{{- if .Logs }}
--- recent logs (last 200 lines, redacted) ---
{{ .Logs }}
{{- end }}
`

const tmplWhy = preamble + `
The following pod is not running as expected. Identify which of these failure
modes applies, citing the evidence:
  - ImagePullBackOff / ErrImagePull (registry, auth, or tag issues)
  - CrashLoopBackOff (the app exits immediately; look at the logs)
  - Pending: unschedulable (resource pressure, node selector, taints)
  - Pending: PVC binding stuck
  - OOMKilled (memory limit too low)
  - readiness/liveness probe failure
  - other (say so explicitly)

If the data is insufficient to pick one, list the next diagnostic command to run.

Resource: {{ .Resource }}

--- describe ---
{{ .Describe }}

{{- if .Events }}
--- recent events ---
{{ .Events }}
{{- end }}

{{- if .Logs }}
--- recent logs ---
{{ .Logs }}
{{- end }}
`

const tmplLogs = preamble + `
Summarize the errors in the following log output. For each distinct error:
  - one-line description
  - first and last timestamps (if visible)
  - the line(s) of evidence
Then give the most likely root cause and one concrete next step.

Resource: {{ .Resource }}

--- logs (last 200 lines, redacted) ---
{{ .Logs }}
`
