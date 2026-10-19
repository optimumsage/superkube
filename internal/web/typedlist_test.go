package web

import (
	"net/http"
	"testing"

	"github.com/optimumsage/superkube/internal/guardrail"
)

func TestApplyForbidSeesTypedListItemsWithoutGVK(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "dev", policy: func(string) (guardrail.Policy, error) {
		return guardrail.Policy{Forbidden: []string{"apply secret"}}, nil
	}})
	list := "apiVersion: v1\nkind: SecretList\nitems:\n- metadata:\n    name: s1\n"
	for _, path := range []string{"/api/v1/apply/preview", "/api/v1/apply/commit"} {
		rr := e.do(t, path, mustJSONStr(map[string]string{"yaml": list, "confirm_token": "x"}))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s: status %d, want 403: %s", path, rr.Code, rr.Body.String())
		}
	}
	docs, err := parseManifestDocs(list)
	if err != nil || len(docs) != 1 || docs[0].Kind != "Secret" || docs[0].APIVersion != "v1" {
		t.Fatalf("docs=%+v err=%v", docs, err)
	}
}
