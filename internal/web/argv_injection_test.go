package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/helm"
)

// doMethod is like do but with an arbitrary method (CSRF cookie attached).
func (e *hardenEnv) doMethod(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "127.0.0.1"
	tok := e.s.csrf.ensureCookie(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: tok})
	req.Header.Set("X-CSRF-Token", tok)
	rr := httptest.NewRecorder()
	e.s.middleware(e.s.mux).ServeHTTP(rr, req)
	return rr
}

func TestFlagShapedInputsRejectedEverywhere(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{})
	e.s.deps.Helm = &helm.Runner{Path: fakeHelmBinary(t)}
	bad := "--kubeconfig=/x"
	seg := "--kubeconfig=x" // path segments cannot hold a slash
	cases := []struct{ method, path, body string }{
		// port-forward
		{"POST", "/api/v1/portforward", `{"target":"pod/web","ports":["8080:80","--context=prod"]}`},
		{"POST", "/api/v1/portforward", `{"target":"--kubeconfig=/x","ports":["80"]}`},
		{"POST", "/api/v1/portforward", `{"target":"web","ports":["80"]}`},
		{"POST", "/api/v1/portforward", `{"target":"pod/web","ports":["80"],"address":"--context=prod"}`},
		{"POST", "/api/v1/portforward", `{"target":"pod/web","ports":["80"],"namespace":"--context=prod"}`},
		// resources
		{"GET", "/api/v1/resources/" + seg + "/default/x/describe", ""},
		{"GET", "/api/v1/resources/pods/" + seg + "/x/describe", ""},
		{"GET", "/api/v1/resources/pods/default/" + seg + "/yaml", ""},
		{"GET", "/api/v1/resources/secrets/default/" + seg + "/yaml?reveal=1", ""},
		{"GET", "/api/v1/resources/pods/default/" + seg + "/events", ""},
		{"GET", "/api/v1/resources/configmaps/default/" + seg + "/form", ""},
		{"POST", "/api/v1/resources/configmaps/" + seg + "/x/edit/preview", `{"yaml":"a: b"}`},
		{"POST", "/api/v1/resources/configmaps/default/" + seg + "/edit/commit", `{"yaml":"a: b"}`},
		// destructive
		{"POST", "/api/v1/destructive/delete", `{"kind":"pod","name":"x","namespace":"` + bad + `"}`},
		{"POST", "/api/v1/destructive/delete", `{"kind":"` + bad + `","name":"x"}`},
		{"POST", "/api/v1/destructive/scale", `{"kind":"deploy","name":"` + bad + `","replicas":1}`},
		{"POST", "/api/v1/destructive/rollout/undo", `{"kind":"deploy","name":"x","namespace":"` + bad + `"}`},
		{"POST", "/api/v1/destructive/cordon", `{"name":"` + bad + `"}`},
		{"POST", "/api/v1/destructive/drain", `{"name":"` + bad + `"}`},
		// AI
		{"POST", "/api/v1/ai/diagnose", `{"kind":"` + bad + `","name":"x"}`},
		{"POST", "/api/v1/ai/why", `{"kind":"pod","name":"` + bad + `"}`},
		{"POST", "/api/v1/ai/why", `{"kind":"pod","name":"x","namespace":"` + bad + `"}`},
		{"POST", "/api/v1/ai/logs", `{"pod":"` + bad + `"}`},
		{"POST", "/api/v1/ai/logs", `{"pod":"x","namespace":"` + bad + `"}`},
		{"POST", "/api/v1/ai/logs", `{"pod":"x","container":"` + bad + `"}`},
		// helm
		{"GET", "/api/v1/helm/releases/" + seg + "/x", ""},
		{"GET", "/api/v1/helm/releases/default/" + seg + "/values", ""},
		{"GET", "/api/v1/helm/releases/default/x/manifest?revision=--kubeconfig=x", ""},
		{"GET", "/api/v1/helm/releases/default/x/history?x=1", ""}, // control: not rejected as bad input
		{"GET", "/api/v1/helm/charts/values?repo=" + seg + "&chart=c", ""},
		{"GET", "/api/v1/helm/charts/values?repo=r&chart=c&version=--kubeconfig=x", ""},
		{"POST", "/api/v1/helm/rollback", `{"name":"` + bad + `","namespace":"d","revision":1}`},
		{"POST", "/api/v1/helm/install/preview", `{"name":"x","namespace":"d","chart":"r/c","version":"--kubeconfig=x"}`},
		{"POST", "/api/v1/helm/upgrade/preview", `{"name":"x","namespace":"d","chart":"` + bad + `"}`},
		{"POST", "/api/v1/helm/repos", `{"name":"r","url":"https://x","username":"` + bad + `"}`},
		{"POST", "/api/v1/helm/repos", `{"name":"` + bad + `","url":"https://x"}`},
	}
	for _, c := range cases {
		rr := e.doMethod(t, c.method, c.path, c.body)
		isControl := strings.Contains(c.path, "history?x=1")
		if isControl {
			if rr.Code == http.StatusBadRequest {
				t.Errorf("control %s rejected: %s", c.path, rr.Body.String())
			}
			continue
		}
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s %s %s: code %d, want 400 (%s)", c.method, c.path, c.body, rr.Code, strings.TrimSpace(rr.Body.String()))
		}
	}
	for _, call := range e.calls() {
		if strings.Contains(call, "--kubeconfig=/x") || strings.Contains(call, "--context=prod") {
			t.Errorf("injected flag reached kubectl: %s", call)
		}
	}
}

func TestHelmSearchTermAfterDoubleDash(t *testing.T) {
	// The term is data: it must follow "--" so it can never parse as a flag.
	e := newHardenEnv(t, hardenOpts{})
	e.s.deps.Helm = &helm.Runner{Path: fakeHelmBinary(t)}
	rr := e.doMethod(t, "GET", "/api/v1/helm/search?term=--kubeconfig=/x", "")
	if strings.Contains(rr.Body.String(), "search repo -o json --kubeconfig=/x") {
		t.Fatalf("term not fenced by --: %s", rr.Body.String())
	}
}

func TestPortForwardPolicySeesEffectiveArgs(t *testing.T) {
	pol := func(string) (guardrail.Policy, error) {
		return guardrail.Policy{Forbidden: []string{"port-forward -n kube-system"}}, nil
	}
	e := newHardenEnv(t, hardenOpts{policy: pol})
	rr := e.do(t, "/api/v1/portforward", `{"target":"pod/web","ports":["80"],"namespace":"kube-system"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("code %d, want 403: %s", rr.Code, rr.Body.String())
	}
}

func TestCronJobTriggerGuarded(t *testing.T) {
	forbid := true
	pol := func(string) (guardrail.Policy, error) {
		if forbid {
			return guardrail.Policy{Forbidden: []string{"create job"}}, nil
		}
		return guardrail.Policy{}, nil
	}
	e := newHardenEnv(t, hardenOpts{policy: pol})
	const path = "/api/v1/destructive/cronjob/trigger"
	if rr := e.do(t, path, `{"name":"--kubeconfig=/x"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("flag-shaped name: %d", rr.Code)
	}
	if rr := e.do(t, path, `{"name":"nightly","namespace":"d"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("forbidden: %d", rr.Code)
	}
	forbid = false
	first := decode[confirmationResponse](t, e.do(t, path, `{"name":"nightly","namespace":"d"}`))
	if first.Status != "needs_confirmation" || len(e.calls()) != 0 {
		t.Fatalf("first = %+v calls=%v", first, e.calls())
	}
	// A token for one cronjob cannot trigger another.
	if rr := e.do(t, path, `{"name":"other","namespace":"d","confirm_token":"`+first.Token+`"}`); rr.Code != http.StatusGone {
		t.Fatalf("mismatched token: %d", rr.Code)
	}
	first = decode[confirmationResponse](t, e.do(t, path, `{"name":"nightly","namespace":"d"}`))
	if rr := e.do(t, path, `{"name":"nightly","namespace":"d","confirm_token":"`+first.Token+`"}`); rr.Code != http.StatusOK {
		t.Fatalf("confirmed: %d %s", rr.Code, rr.Body.String())
	}
	c := e.calls()
	if len(c) != 1 || !strings.Contains(c[0], "create job nightly-manual-") || !strings.Contains(c[0], "--from=cronjob/nightly") || !strings.Contains(c[0], "-n d") {
		t.Fatalf("calls = %q", c)
	}
	if len(e.events) == 0 {
		t.Fatal("no audit event")
	}
}
