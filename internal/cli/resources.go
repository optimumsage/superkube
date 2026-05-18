package cli

import "strings"

// knownResources is the set of built-in kubectl resource names (singular,
// plural, and short forms) we'll treat as implicit `get` targets. Used to
// support `sk pods` as shorthand for `sk get pods`. Tokens not in this set
// fall through to passthrough so krew plugins and future kubectl verbs keep
// working — we only auto-insert `get` when we're certain the token names a
// resource.
//
// Deliberately excluded: `events` / `ev`. kubectl now has a top-level `events`
// subcommand whose output differs from `kubectl get events`; leaving it out
// preserves passthrough to that subcommand.
var knownResources = map[string]bool{
	// core/v1
	"pods": true, "pod": true, "po": true,
	"services": true, "service": true, "svc": true,
	"nodes": true, "node": true, "no": true,
	"namespaces": true, "namespace": true, "ns": true,
	"configmaps": true, "configmap": true, "cm": true,
	"secrets": true, "secret": true,
	"endpoints": true, "endpoint": true, "ep": true,
	"persistentvolumes": true, "persistentvolume": true, "pv": true,
	"persistentvolumeclaims": true, "persistentvolumeclaim": true, "pvc": true,
	"serviceaccounts": true, "serviceaccount": true, "sa": true,
	"replicationcontrollers": true, "replicationcontroller": true, "rc": true,
	"limitranges": true, "limitrange": true, "limits": true,
	"resourcequotas": true, "resourcequota": true, "quota": true,
	"componentstatuses": true, "componentstatus": true, "cs": true,
	"podtemplates": true, "podtemplate": true,

	// apps
	"deployments": true, "deployment": true, "deploy": true,
	"replicasets": true, "replicaset": true, "rs": true,
	"daemonsets": true, "daemonset": true, "ds": true,
	"statefulsets": true, "statefulset": true, "sts": true,
	"controllerrevisions": true, "controllerrevision": true,

	// batch
	"jobs": true, "job": true,
	"cronjobs": true, "cronjob": true, "cj": true,

	// autoscaling
	"horizontalpodautoscalers": true, "horizontalpodautoscaler": true, "hpa": true,

	// policy
	"poddisruptionbudgets": true, "poddisruptionbudget": true, "pdb": true,

	// networking
	"ingresses": true, "ingress": true, "ing": true,
	"ingressclasses": true, "ingressclass": true,
	"networkpolicies": true, "networkpolicy": true, "netpol": true,

	// rbac
	"roles": true, "role": true,
	"rolebindings": true, "rolebinding": true,
	"clusterroles": true, "clusterrole": true,
	"clusterrolebindings": true, "clusterrolebinding": true,

	// storage
	"storageclasses": true, "storageclass": true, "sc": true,
	"volumeattachments": true, "volumeattachment": true,
	"csidrivers": true, "csidriver": true,
	"csinodes": true, "csinode": true,
	"csistoragecapacities": true, "csistoragecapacity": true,

	// scheduling
	"priorityclasses": true, "priorityclass": true, "pc": true,

	// apiextensions
	"customresourcedefinitions": true, "customresourcedefinition": true,
	"crds": true, "crd": true,

	// discovery
	"endpointslices": true, "endpointslice": true,

	// certificates
	"certificatesigningrequests": true, "certificatesigningrequest": true, "csr": true,

	// coordination
	"leases": true, "lease": true,

	// flowcontrol
	"flowschemas": true, "flowschema": true,
	"prioritylevelconfigurations": true, "prioritylevelconfiguration": true,

	// kubectl pseudo-resource
	"all": true,
}

// looksLikeResource reports whether token names a known kubectl resource that
// `sk` should auto-prefix with `get`. Accepts:
//
//   - bare names: `pods`, `svc`, `deploy`
//   - kind/name pairs: `pod/foo`, `deploy/web`
//   - comma-joined lists: `pods,svc` (only when every part is a known resource)
//
// Anything else (krew plugin names, kubectl verbs, typos) returns false so the
// caller can fall through to passthrough.
func looksLikeResource(token string) bool {
	if token == "" {
		return false
	}
	candidate := token
	if slash := strings.IndexByte(candidate, '/'); slash > 0 {
		candidate = candidate[:slash]
	}
	if strings.Contains(candidate, ",") {
		for _, p := range strings.Split(candidate, ",") {
			if !knownResources[p] {
				return false
			}
		}
		return true
	}
	return knownResources[candidate]
}

// insertVerbAt returns a copy of args with verb spliced in at position idx.
// Used to rewrite `sk pods -n test` → `sk get pods -n test` while leaving any
// leading root flags (`sk -n test pods` → `sk -n test get pods`) in place.
func insertVerbAt(args []string, idx int, verb string) []string {
	out := make([]string, 0, len(args)+1)
	out = append(out, args[:idx]...)
	out = append(out, verb)
	out = append(out, args[idx:]...)
	return out
}
