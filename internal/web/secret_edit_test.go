package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/optimumsage/superkube/internal/guardrail"
)

const rawSecretYAML = "apiVersion: v1\nkind: Secret\nmetadata:\n  name: a\n  namespace: default\ndata:\n  pw: c2VjcmV0\n"

const secretScript = `case " $* " in
*" diff "*) echo '+changed'; exit 1;;
esac
cat <<'EOF'
` + rawSecretYAML + `EOF
`

const secretBase = "/api/v1/resources/secrets/default/a/"

func TestSecretEditLoadFlow(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "dev", script: secretScript})

	// The view stays masked.
	view := e.doMethod(t, "GET", secretBase+"yaml", "").Body.String()
	if strings.Contains(view, "c2VjcmV0") {
		t.Fatalf("view leaked:\n%s", view)
	}

	// The editor's explicit raw load returns the real manifest...
	rr := e.doMethod(t, "GET", secretBase+"yaml?edit=1", "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "pw: c2VjcmV0") {
		t.Fatalf("edit load: %d\n%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "hidden") {
		t.Fatalf("edit load is masked:\n%s", rr.Body.String())
	}
	// ...and is audited.
	e.mu.Lock()
	var audited bool
	for _, ev := range e.events {
		if strings.HasSuffix(ev.Cmd, "secret-edit-load") && strings.Contains(strings.Join(ev.Argv, " "), "default/a") {
			audited = true
		}
	}
	e.mu.Unlock()
	if !audited {
		t.Fatalf("secret-edit-load not audited: %+v", e.events)
	}

	// Metadata edit on the loaded buffer previews cleanly.
	edited := strings.Replace(rr.Body.String(), "namespace: default\n", "namespace: default\n  labels:\n    x: y\n", 1)
	pv := e.do(t, secretBase+"edit/preview", mustJSONStr(map[string]string{"yaml": edited}))
	if pv.Code != http.StatusOK || decode[map[string]any](t, pv)["confirm_token"] == nil {
		t.Fatalf("preview: %d %s", pv.Code, pv.Body.String())
	}
}

func TestSecretEditLoadGatedByEditPolicy(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "dev", script: secretScript, policy: func(string) (guardrail.Policy, error) {
		return guardrail.Policy{Forbidden: []string{"edit secret"}}, nil
	}})
	rr := e.doMethod(t, "GET", secretBase+"yaml?edit=1", "")
	if rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), "c2VjcmV0") {
		t.Fatalf("forbidden edit load: %d %s", rr.Code, rr.Body.String())
	}
}
