package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/optimumsage/superkube/internal/guardrail"
)

// strictNSKubectl mimics real kubectl: a -n that disagrees with a manifest's
// metadata.namespace is an error (exit 1 for diff too).
const strictNSKubectl = `ns=""; file=""; prev=""
for a in "$@"; do
  [ "$prev" = -n ] && ns="$a"
  [ "$prev" = -f ] && file="$a"
  prev="$a"
done
if [ -n "$ns" ] && [ -n "$file" ]; then
  for m in $(grep -E '^ +namespace: ' "$file" | awk '{print $2}'); do
    if [ "$m" != "$ns" ]; then
      echo "error: the namespace from the provided object \"$m\" does not match the namespace \"$ns\". You must pass '--namespace=$m' to perform this operation." >&2
      exit 1
    fi
  done
fi
if [ "$1" = diff ]; then echo '+changed'; exit 1; fi
echo ok
`

type previewResp struct {
	Status string `json:"status"`
	Token  string `json:"confirm_token"`
}

const nsCM = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: other\ndata:\n  a: b\n"

func jsonBody(kv ...string) string {
	var parts []string
	for i := 0; i < len(kv); i += 2 {
		parts = append(parts, `"`+kv[i]+`":`+kv[i+1])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func TestApplyManifestNamespaceNotOverridden(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{script: strictNSKubectl})
	rr := e.do(t, "/api/v1/apply/preview", jsonBody("yaml", quote(nsCM)))
	if rr.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	r := decode[previewResp](t, rr)
	if r.Status != "needs_confirmation" {
		t.Fatalf("preview: %+v", r)
	}
	rr = e.do(t, "/api/v1/apply/commit", jsonBody("yaml", quote(nsCM), "confirm_token", quote(r.Token), "confirm_value", `"APPLY"`))
	if rr.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", rr.Code, rr.Body.String())
	}
	// A namespace-less doc still gets the resolved namespace.
	noNS := strings.Replace(nsCM, "  namespace: other\n", "", 1)
	e.do(t, "/api/v1/apply/preview", jsonBody("yaml", quote(noNS)))
	last := e.calls()[len(e.calls())-1]
	if !strings.Contains(last, "-n default") {
		t.Fatalf("namespace-less manifest lost -n: %q", last)
	}
}

func TestDiffErrorIsNotChanges(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{script: strictNSKubectl})
	// namespace-less doc but the cookie namespace is fine; force an error via a
	// mixed manifest that real kubectl rejects.
	mixed := nsCM + "---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: b\ndata:\n  a: b\n"
	rr := e.do(t, "/api/v1/apply/preview", jsonBody("yaml", quote(mixed)))
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "does not match") {
		t.Fatalf("diff error must be a 502 with stderr, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestEditUsesURLNamespace(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{script: strictNSKubectl})
	path := "/api/v1/resources/configmaps/other/app/edit"
	y := quote(nsCM)
	rr := e.do(t, path+"/preview", jsonBody("yaml", y), &http.Cookie{Name: sessNsCookie, Value: "default"})
	r := decode[previewResp](t, rr)
	if rr.Code != http.StatusOK || r.Status != "needs_confirmation" {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	rr = e.do(t, path+"/commit", jsonBody("yaml", y, "confirm_token", quote(r.Token)), &http.Cookie{Name: sessNsCookie, Value: "default"})
	if rr.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", rr.Code, rr.Body.String())
	}
}

const secretYAML = "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: default\nstringData:\n  a: b\n"

func forbid(rules ...string) func(string) (guardrail.Policy, error) {
	return func(string) (guardrail.Policy, error) { return guardrail.Policy{Forbidden: rules}, nil }
}

func TestListItemsAreGated(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{policy: forbid("apply secret")})
	list := "apiVersion: v1\nkind: List\nitems:\n  - apiVersion: v1\n    kind: Secret\n    metadata:\n      name: s\n      namespace: default\n"
	for _, p := range []string{"/api/v1/apply/preview", "/api/v1/apply/commit"} {
		rr := e.do(t, p, jsonBody("yaml", quote(list)))
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s", p, rr.Code, rr.Body.String())
		}
	}
	nested := "kind: ConfigMapList\nitems:\n  - kind: List\n    items:\n      - kind: Secret\n        metadata: {name: s}\n"
	if rr := e.do(t, "/api/v1/apply/preview", jsonBody("yaml", quote(nested))); rr.Code != http.StatusForbidden {
		t.Errorf("nested list: %d", rr.Code)
	}
}

func TestEditHonoursApplyNamespaceRule(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{policy: forbid("apply -n kube-system")})
	cm := strings.Replace(nsCM, "other", "kube-system", 1)
	for _, p := range []string{"preview", "commit"} {
		rr := e.do(t, "/api/v1/resources/configmaps/kube-system/app/edit/"+p, jsonBody("yaml", quote(cm)))
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s", p, rr.Code, rr.Body.String())
		}
	}
}

func TestEditHonoursEditKindRule(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{policy: forbid("edit secret")})
	rr := e.do(t, "/api/v1/resources/secrets/default/s/edit/preview", jsonBody("yaml", quote(secretYAML)))
	if rr.Code != http.StatusForbidden {
		t.Errorf("%d %s", rr.Code, rr.Body.String())
	}
}
