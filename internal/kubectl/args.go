package kubectl

import "strings"

// PrependGlobalFlags adds kubectl-global flags (--context, --namespace,
// --kubeconfig) to the front of an argv. Existing copies of the same flag in
// argv take precedence (we don't add a flag if the user already passed one).
//
// Used both by passthrough (so `sk --context prod kustomize ...` propagates the
// context) and by enhanced commands that build their own kubectl args.
func PrependGlobalFlags(args []string, kubeContext, namespace, kubeconfig string) []string {
	out := args
	if kubeContext != "" && !hasFlag(out, "--context") {
		out = append([]string{"--context", kubeContext}, out...)
	}
	if namespace != "" && !hasFlag(out, "--namespace") && !hasFlag(out, "-n") {
		out = append([]string{"--namespace", namespace}, out...)
	}
	if kubeconfig != "" && !hasFlag(out, "--kubeconfig") {
		out = append([]string{"--kubeconfig", kubeconfig}, out...)
	}
	return out
}

// hasFlag reports whether args contains the given flag name, in any of these
// forms: `--name`, `--name=value`, `--name value`, `-x`, `-x=value`, `-x value`.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}
