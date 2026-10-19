package ai

import (
	"strings"
	"testing"
)

// globMatch mirrors Claude's `*` wildcard (matches any run of characters,
// including spaces and none), anchored at both ends.
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

func deniedByRules(cmd string) bool {
	for _, r := range claudeDeniedTools {
		pat := strings.TrimSuffix(strings.TrimPrefix(r, "Bash("), ")")
		if globMatch(pat, cmd) {
			return true
		}
	}
	return false
}

func TestClaudeDenied_BehavioralSecretsAnyCase(t *testing.T) {
	denied := []string{
		"kubectl get secrets -A -o yaml",
		"kubectl get SECRETS -A -o yaml",
		"kubectl get SeCrEt foo -o json",
		"kubectl get Secret/foo",
		"kubectl get secrets.v1 -n default",
		"kubectl get -n default secrets -o yaml",
		"kubectl describe SECRET foo",
		"kubectl get cm,secrets",
		"sk get SECRETS -o yaml",
		"sk describe sEcReT x",
		"kubectl config view --raw",
		"kubectl --context x config view",
		"kubectl get --raw /api/v1",
	}
	for _, c := range denied {
		if !deniedByRules(c) {
			t.Errorf("command not denied: %q", c)
		}
	}
	allowed := []string{
		"kubectl get pods -n default -o yaml",
		"kubectl logs web-1",
		"kubectl describe pod web-1",
		"sk get deploy",
	}
	for _, c := range allowed {
		if deniedByRules(c) {
			t.Errorf("command wrongly denied: %q", c)
		}
	}
}
