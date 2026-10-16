package web

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/optimumsage/superkube/internal/audit"
	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/kubectl"
)

// hardenEnv is a Server wired to a fake kubectl that records every argv.
type hardenEnv struct {
	s      *Server
	log    string
	mu     sync.Mutex
	events []audit.Event
}

type hardenOpts struct {
	policy     func(ctx string) (guardrail.Policy, error)
	yes        bool
	token      string
	bind       string
	startCtx   string
	noRunner   bool
	kubeconfig string
}

func newHardenEnv(t *testing.T, o hardenOpts) *hardenEnv {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "kubectl")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\n" +
		"if [ \"$1\" = diff ]; then echo '+changed'; exit 1; fi\necho ok\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	kc := o.kubeconfig
	if kc == "" {
		kc = filepath.Join(dir, "no-kubeconfig") // hermetic: never read the host kubeconfig
	}
	e := &hardenEnv{log: logPath}
	deps := Deps{
		Loader:     kube.Loader{Context: o.startCtx, KubeconfigPath: kc},
		Kubeconfig: kc,
		PolicyFor:  o.policy,
		Audit: func(ev audit.Event) {
			e.mu.Lock()
			e.events = append(e.events, ev)
			e.mu.Unlock()
		},
		Context: o.startCtx,
		Yes:     o.yes,
	}
	if !o.noRunner {
		deps.Runner = &kubectl.Runner{Path: bin}
	}
	bind := o.bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	s, err := New(Config{Bind: bind, Token: o.token, NoOpen: true}, deps)
	if err != nil {
		t.Fatal(err)
	}
	e.s = s
	return e
}

// calls returns the recorded kubectl argv lines.
func (e *hardenEnv) calls() []string {
	b, _ := os.ReadFile(e.log)
	s := strings.TrimSpace(string(b))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func (e *hardenEnv) do(t *testing.T, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Host = "127.0.0.1"
	tok := e.s.csrf.ensureCookie(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: tok})
	req.Header.Set("X-CSRF-Token", tok)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	e.s.middleware(e.s.mux).ServeHTTP(rr, req)
	return rr
}

func ctxCookie(v string) *http.Cookie { return &http.Cookie{Name: sessCtxCookie, Value: v} }

func decode[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
	return v
}

// --- 1 / 2: session context drives loader, policy, banner and audit ---------

func TestLoaderFollowsSessionContext(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "start"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(ctxCookie("switched"))
	sess := e.s.readSession(req)
	if got := e.s.loader(sess).Context; got != "switched" {
		t.Fatalf("loader context = %q, want the session's %q", got, "switched")
	}
}

func prodForbidsDelete(ctx string) (guardrail.Policy, error) {
	if ctx == "prod" {
		return guardrail.Policy{Forbidden: []string{"delete"}}, nil
	}
	return guardrail.Policy{}, nil
}

func TestPolicyFollowsContextSwitch(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "dev", policy: prodForbidsDelete})
	body := `{"kind":"pod","name":"foo","namespace":"default"}`

	rr := e.do(t, "/api/v1/destructive/delete", body)
	if r := decode[confirmationResponse](t, rr); r.Status != "needs_confirmation" {
		t.Fatalf("dev: got %+v", r)
	}
	rr = e.do(t, "/api/v1/destructive/delete", body, ctxCookie("prod"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("prod after UI switch: status %d, want 403 (%s)", rr.Code, rr.Body.String())
	}
}

func TestAuditRecordsSessionContext(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "dev"})
	body := `{"kind":"deployment","name":"web","namespace":"default","action":"restart"}`
	rr := e.do(t, "/api/v1/destructive/rollout/restart", body, ctxCookie("staging"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.events) != 1 || e.events[0].Context != "staging" {
		t.Fatalf("audit events = %+v, want one with Context=staging", e.events)
	}
	if c := e.calls(); len(c) != 1 || !strings.Contains(c[0], "--context staging") {
		t.Fatalf("kubectl calls = %v", c)
	}
}

func TestPolicyLoadErrorRefusesDestructive(t *testing.T) {
	boom := func(string) (guardrail.Policy, error) { return guardrail.Policy{}, errors.New("bad yaml") }
	e := newHardenEnv(t, hardenOpts{startCtx: "dev", policy: boom})
	rr := e.do(t, "/api/v1/destructive/cordon", `{"name":"n1"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rr.Code)
	}
	if r := decode[confirmationResponse](t, rr); r.Status != "blocked" || !strings.Contains(r.Detail, "bad yaml") {
		t.Fatalf("got %+v", r)
	}
	if len(e.calls()) != 0 {
		t.Fatal("kubectl ran despite unreadable policy")
	}
}

// --- 3: tokens are bound to the operation ----------------------------------

func TestCordonTokenCannotAuthorizeApply(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "dev"})
	r := decode[confirmationResponse](t, e.do(t, "/api/v1/destructive/cordon", `{"name":"n1"}`))
	if r.Token == "" {
		t.Fatalf("no token: %+v", r)
	}
	rr := e.do(t, "/api/v1/apply/commit",
		`{"yaml":"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: evil\n","confirm_token":"`+r.Token+`"}`)
	if rr.Code != http.StatusGone {
		t.Fatalf("status %d, want 410", rr.Code)
	}
	if len(e.calls()) != 0 {
		t.Fatalf("kubectl ran: %v", e.calls())
	}
}

const cmYAML = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: default\ndata:\n  a: b\n"

func TestApplyCommitMustMatchPreviewedYAML(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "dev"})
	pv := e.do(t, "/api/v1/apply/preview", mustJSONStr(map[string]string{"yaml": cmYAML}))
	tok := decode[map[string]any](t, pv)["confirm_token"].(string)

	other := strings.Replace(cmYAML, "a: b", "a: HACKED", 1)
	rr := e.do(t, "/api/v1/apply/commit", mustJSONStr(map[string]string{"yaml": other, "confirm_token": tok}))
	if rr.Code != http.StatusGone {
		t.Fatalf("changed YAML: status %d, want 410", rr.Code)
	}
	for _, c := range e.calls() {
		if strings.HasPrefix(c, "--context") && strings.Contains(c, " apply ") {
			t.Fatalf("apply ran: %s", c)
		}
	}

	// Token is burned by the failed attempt; a fresh preview+commit works.
	pv = e.do(t, "/api/v1/apply/preview", mustJSONStr(map[string]string{"yaml": cmYAML}))
	tok = decode[map[string]any](t, pv)["confirm_token"].(string)
	rr = e.do(t, "/api/v1/apply/commit", mustJSONStr(map[string]string{"yaml": cmYAML, "confirm_token": tok}))
	if rr.Code != http.StatusOK {
		t.Fatalf("matching commit: status %d: %s", rr.Code, rr.Body.String())
	}
}

func mustJSONStr(v any) string { return string(mustJSON(v)) }

func TestEditCommitChecksTargetAndPolicy(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "dev", policy: func(ctx string) (guardrail.Policy, error) {
		if ctx == "prod" {
			return guardrail.Policy{Forbidden: []string{"edit"}}, nil
		}
		return guardrail.Policy{}, nil
	}})
	base := "/api/v1/resources/configmaps/default/app/edit/"

	// Preview of a manifest naming a different object is rejected.
	bad := strings.Replace(cmYAML, "name: app", "name: other", 1)
	rr := e.do(t, base+"preview", mustJSONStr(map[string]string{"yaml": bad}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("mismatched name preview: %d %s", rr.Code, rr.Body.String())
	}
	// Wrong kind too.
	rr = e.do(t, base+"preview", mustJSONStr(map[string]string{"yaml": strings.Replace(cmYAML, "ConfigMap", "Secret", 1)}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("mismatched kind preview: %d", rr.Code)
	}

	// Good preview → token; commit after switching to a forbidden context is refused.
	tok := decode[map[string]any](t, e.do(t, base+"preview", mustJSONStr(map[string]string{"yaml": cmYAML})))["confirm_token"].(string)
	rr = e.do(t, base+"commit", mustJSONStr(map[string]string{"yaml": cmYAML, "confirm_token": tok}), ctxCookie("prod"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("commit in forbidden ctx: %d %s", rr.Code, rr.Body.String())
	}
	// The same token is also bound to the context it was minted for.
	tok = decode[map[string]any](t, e.do(t, base+"preview", mustJSONStr(map[string]string{"yaml": cmYAML})))["confirm_token"].(string)
	rr = e.do(t, base+"commit", mustJSONStr(map[string]string{"yaml": cmYAML, "confirm_token": tok}), ctxCookie("staging"))
	if rr.Code != http.StatusGone {
		t.Fatalf("commit in other ctx: %d, want 410", rr.Code)
	}
}

func TestValidateEditYAML(t *testing.T) {
	ok := func(y string) error { return validateEditYAML("cm", "default", "app", "default", y) }
	if err := ok(cmYAML); err != nil {
		t.Fatalf("valid yaml rejected: %v", err)
	}
	if err := ok(strings.Replace(cmYAML, "  namespace: default\n", "", 1)); err != nil {
		t.Fatalf("namespace defaulting to session ns rejected: %v", err)
	}
	for name, y := range map[string]string{
		"two docs":  cmYAML + "---\n" + cmYAML,
		"other ns":  strings.Replace(cmYAML, "namespace: default", "namespace: kube-system", 1),
		"other nm":  strings.Replace(cmYAML, "name: app", "name: x", 1),
		"bad yaml":  "kind: [",
		"wrong knd": strings.Replace(cmYAML, "ConfigMap", "Pod", 1),
	} {
		if ok(y) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if validateEditYAML("cm", "default", "app", "other-ns", strings.Replace(cmYAML, "  namespace: default\n", "", 1)) == nil {
		t.Error("missing namespace must not default when the session ns differs from the URL ns")
	}
}

func TestDeleteTokenBoundToName(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "dev"})
	r := decode[confirmationResponse](t, e.do(t, "/api/v1/destructive/delete", `{"kind":"pod","name":"foo","namespace":"default"}`))
	rr := e.do(t, "/api/v1/destructive/delete",
		`{"kind":"pod","name":"bar","namespace":"default","confirm_token":"`+r.Token+`","confirm_value":"foo"}`)
	if rr.Code != http.StatusGone {
		t.Fatalf("status %d, want 410", rr.Code)
	}
	if len(e.calls()) != 0 {
		t.Fatalf("kubectl ran: %v", e.calls())
	}
}

// --- 4: client "yes" never skips confirmation -------------------------------

func TestClientYesIsIgnored(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "dev"})
	for path, body := range map[string]string{
		"/api/v1/destructive/delete":       `{"kind":"pod","name":"foo","all":true,"namespace":"default","yes":true}`,
		"/api/v1/destructive/drain":        `{"name":"n1","yes":true}`,
		"/api/v1/destructive/cordon":       `{"name":"n1","yes":true}`,
		"/api/v1/destructive/scale":        `{"kind":"deployment","name":"w","replicas":0,"yes":true}`,
		"/api/v1/destructive/rollout/undo": `{"kind":"deployment","name":"w","yes":true}`,
	} {
		rr := e.do(t, path, body)
		if r := decode[confirmationResponse](t, rr); r.Status != "needs_confirmation" {
			t.Errorf("%s: yes:true skipped confirmation: %+v", path, r)
		}
	}
	if len(e.calls()) != 0 {
		t.Fatalf("kubectl ran: %v", e.calls())
	}
}

func TestServerYesSkipsExceptAllAndForbidden(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "dev", yes: true, policy: func(string) (guardrail.Policy, error) {
		return guardrail.Policy{Forbidden: []string{"drain"}}, nil
	}})
	if rr := e.do(t, "/api/v1/destructive/cordon", `{"name":"n1"}`); rr.Code != http.StatusOK {
		t.Fatalf("server --yes cordon: %d %s", rr.Code, rr.Body.String())
	}
	r := decode[confirmationResponse](t, e.do(t, "/api/v1/destructive/delete", `{"kind":"pod","all":true,"namespace":"default"}`))
	if r.Status != "needs_confirmation" || r.Style != "typed_phrase" {
		t.Fatalf("delete --all must still confirm under --yes: %+v", r)
	}
	if rr := e.do(t, "/api/v1/destructive/drain", `{"name":"n1"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("forbidden drain under --yes: %d", rr.Code)
	}
}

// --- 5: RequireTypedConfirm --------------------------------------------------

func TestRequireTypedConfirmUpgradesYesNo(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "dev", policy: func(string) (guardrail.Policy, error) {
		return guardrail.Policy{RequireTypedConfirm: true}, nil
	}})
	r := decode[confirmationResponse](t, e.do(t, "/api/v1/destructive/cordon", `{"name":"n1"}`))
	if r.Style != "typed_name" || r.Expect != "n1" {
		t.Fatalf("cordon: %+v", r)
	}
	// Commit with a wrong typed value is refused; right value runs.
	rr := e.do(t, "/api/v1/destructive/cordon", `{"name":"n1","confirm_token":"`+r.Token+`","confirm_value":"x"}`)
	if rr.Code != http.StatusGone {
		t.Fatalf("wrong typed value: %d", rr.Code)
	}
	r = decode[confirmationResponse](t, e.do(t, "/api/v1/destructive/scale", `{"kind":"deployment","name":"w","replicas":0}`))
	if r.Style != "typed_name" || r.Expect != "w" {
		t.Fatalf("scale: %+v", r)
	}
	rr = e.do(t, "/api/v1/destructive/scale",
		`{"kind":"deployment","name":"w","replicas":0,"confirm_token":"`+r.Token+`","confirm_value":"w"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("typed scale: %d %s", rr.Code, rr.Body.String())
	}
}

// --- 6: exec policy -----------------------------------------------------------

func TestExecForbiddenByPolicy(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{startCtx: "dev", policy: prodForbidsDelete})
	pol := func(ctx string) (guardrail.Policy, error) {
		if ctx == "prod" {
			return guardrail.Policy{Forbidden: []string{"exec"}}, nil
		}
		return guardrail.Policy{}, nil
	}
	e.s.deps.PolicyFor = pol
	req := httptest.NewRequest(http.MethodGet, "/ws/exec/default/web-1", nil)
	req.Host = "127.0.0.1"
	req.AddCookie(ctxCookie("prod"))
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	rr := httptest.NewRecorder()
	e.s.middleware(e.s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403 before upgrade (%s)", rr.Code, rr.Body.String())
	}
}

// --- 7: wildcard binds --------------------------------------------------------

func TestWildcardBindAllowsInterfaceHosts(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("192.168.1.5"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
	}
	hosts := computeAllowedHosts("0.0.0.0", addrs, "mybox")
	has := func(h string) bool {
		for _, x := range hosts {
			if x == h {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"192.168.1.5", "mybox", "localhost", "127.0.0.1"} {
		if !has(want) {
			t.Errorf("missing %q in %v", want, hosts)
		}
	}
	if has("0.0.0.0") || has("fe80::1") {
		t.Errorf("unexpected entries in %v", hosts)
	}

	e := newHardenEnv(t, hardenOpts{bind: "0.0.0.0", token: "tok"})
	e.s.hosts = hosts
	for host, want := range map[string]int{
		"192.168.1.5:8080": http.StatusOK,
		"mybox:8080":       http.StatusOK,
		"evil.example":     http.StatusForbidden,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/upgrade/check?token=tok", nil)
		req.Host = host
		rr := httptest.NewRecorder()
		e.s.middleware(e.s.mux).ServeHTTP(rr, req)
		if rr.Code != want {
			t.Errorf("Host %s: %d, want %d", host, rr.Code, want)
		}
	}
	// Token still mandatory on the allowed host.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/upgrade/check", nil)
	req.Host = "192.168.1.5:8080"
	rr := httptest.NewRecorder()
	e.s.middleware(e.s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", rr.Code)
	}
}

func TestLoopbackBindDoesNotAllowForeignHosts(t *testing.T) {
	for _, h := range computeAllowedHosts("127.0.0.1", []net.Addr{&net.IPNet{IP: net.ParseIP("10.0.0.2")}}, "box") {
		if h == "10.0.0.2" || h == "box" {
			t.Fatalf("loopback bind allows %q", h)
		}
	}
}

func TestIPv6LoopbackHostAccepted(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/upgrade/check", nil)
	req.Host = "[::1]:8080"
	rr := httptest.NewRecorder()
	e.s.middleware(e.s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
}

func TestOriginPatternsEscapeIPv6(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{})
	e.s.addr = "127.0.0.1:9999"
	var found bool
	for _, p := range e.s.originPatterns() {
		if p == `\[::1\]:9999` {
			found = true
		}
	}
	if !found {
		t.Fatalf("patterns = %v", e.s.originPatterns())
	}
}

func TestStartupBannerHidesTokenWithoutTTY(t *testing.T) {
	for _, tty := range []bool{false, true} {
		buf := &lockedBuf{}
		oldOut, oldTTY := startupOut, stderrIsTTY
		startupOut, stderrIsTTY = buf, func() bool { return tty }
		e := newHardenEnv(t, hardenOpts{token: "sekrit"})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- e.s.Run(ctx) }()
		buf.waitFor(t, "listening")
		cancel()
		<-done
		startupOut, stderrIsTTY = oldOut, oldTTY
		if got := strings.Contains(buf.String(), "sekrit"); got != tty {
			t.Errorf("tty=%v: token in banner = %v (%q)", tty, got, buf.String())
		}
	}
}

// --- 8: limits ------------------------------------------------------------------

func TestOversizedBodyRejected(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{})
	big := strings.Repeat("a", maxBodySmall+1)
	rr := e.do(t, "/api/v1/destructive/cordon", `{"name":"`+big+`"}`)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("small route: %d, want 413", rr.Code)
	}
	// YAML routes allow far more than the small cap.
	y := strings.Repeat("# c\n", (maxBodySmall*4)/4) + cmYAML
	rr = e.do(t, "/api/v1/apply/preview", mustJSONStr(map[string]string{"yaml": y}))
	if rr.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("apply route rejected a %d byte body", len(y))
	}
	huge := strings.Repeat("a", maxBodyYAML+1)
	rr = e.do(t, "/api/v1/apply/preview", `{"yaml":"`+huge+`"}`)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("yaml route: %d, want 413", rr.Code)
	}
}

func TestChunkedBodyIsCapped(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/destructive/cordon",
		strings.NewReader(`{"name":"`+strings.Repeat("a", maxBodySmall+10)+`"}`))
	req.ContentLength = -1
	req.Host = "127.0.0.1"
	tok := e.s.csrf.ensureCookie(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: tok})
	req.Header.Set("X-CSRF-Token", tok)
	rr := httptest.NewRecorder()
	e.s.middleware(e.s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (body capped)", rr.Code)
	}
	if len(e.calls()) != 0 {
		t.Fatal("kubectl ran")
	}
}

func TestServerTimeoutsConfigured(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler())
	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.WriteTimeout == 0 || srv.IdleTimeout == 0 {
		t.Fatalf("missing timeout: %+v", srv)
	}
}

// --- 9: token handling -----------------------------------------------------------

func TestTokenCookieIsHttpOnlyAndInfoOmitsToken(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{bind: "0.0.0.0", token: "tok"})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/info?token=tok", nil)
	req.Host = "localhost"
	rr := httptest.NewRecorder()
	e.s.middleware(e.s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var tokCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "sk_token" {
			tokCookie = c
		}
	}
	if tokCookie == nil || !tokCookie.HttpOnly || tokCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("sk_token cookie = %+v", tokCookie)
	}
	if strings.Contains(rr.Body.String(), "tok\"") || strings.Contains(rr.Body.String(), `"token"`) {
		t.Fatalf("info leaks token: %s", rr.Body.String())
	}
}

// --- 10: CSRF -----------------------------------------------------------------------

func TestCSRFPlantedCookieRejected(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/destructive/cordon", strings.NewReader(`{"name":"n"}`))
	req.Host = "127.0.0.1"
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "planted"})
	req.Header.Set("X-CSRF-Token", "planted")
	rr := httptest.NewRecorder()
	e.s.middleware(e.s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("planted pair accepted: %d", rr.Code)
	}
}

func TestCSRFQueryParamNotAccepted(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{})
	tok := e.s.csrf.ensureCookie(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/destructive/cordon?csrf="+tok, strings.NewReader(`{"name":"n"}`))
	req.Host = "127.0.0.1"
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: tok})
	rr := httptest.NewRecorder()
	e.s.middleware(e.s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("?csrf= accepted: %d", rr.Code)
	}
}

// --- 11: palette allowlist ------------------------------------------------------------

func TestPalettePassthroughAllowlist(t *testing.T) {
	cases := map[string]bool{
		"get pods":                   true,
		"describe pod x":             true,
		"config get-contexts":        true,
		"config current-context":     true,
		"config view":                true,
		"config view --minify":       true,
		"config view --raw":          false,
		"config view --raw=true":     false,
		"config view --flatten":      false,
		"config set-credentials x":   false,
		"config delete-context prod": false,
		"config use-context prod":    false,
		"config":                     false,
		"auth can-i get pods":        true,
		"auth whoami":                true,
		"auth reconcile -f x.yaml":   false,
		"auth":                       false,
		"delete pod x":               false,
		"apply -f x":                 false,
		"exec -it x -- sh":           false,
	}
	for cmd, want := range cases {
		if got := palettePassthroughAllowed(strings.Fields(cmd)); got != want {
			t.Errorf("%q: allowed=%v want %v", cmd, got, want)
		}
	}
}

// --- 13: misc -----------------------------------------------------------------------------

func TestVerbTitle(t *testing.T) {
	if verbTitle("cordon") != "Cordon" || verbTitle("") != "" {
		t.Fatal("verbTitle")
	}
}

func TestBadTokenRejectsFlagShapedNames(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{})
	rr := e.do(t, "/api/v1/destructive/delete", `{"kind":"pod","name":"--all"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rr.Code)
	}
}

// lockedBuf is a goroutine-safe buffer for capturing the startup line.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *lockedBuf) waitFor(t *testing.T, s string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if strings.Contains(l.String(), s) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q", s)
}

func TestApplyAndEditRequireTypedConfirmUnderPolicy(t *testing.T) {
	typed := func(string) (guardrail.Policy, error) { return guardrail.Policy{RequireTypedConfirm: true}, nil }
	e := newHardenEnv(t, hardenOpts{policy: typed})
	y := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: default\n"
	type pv struct {
		Status, Style, Expect, Token string
		ConfirmToken                 string `json:"confirm_token"`
	}
	for _, c := range []struct{ prev, commit, expect string }{
		{"/api/v1/apply/preview", "/api/v1/apply/commit", "APPLY"},
		{"/api/v1/resources/configmaps/default/app/edit/preview", "/api/v1/resources/configmaps/default/app/edit/commit", "app"},
	} {
		body := mustJSONStr(map[string]string{"yaml": y})
		p := decode[pv](t, e.do(t, c.prev, body))
		if p.Expect != c.expect || p.Style != "typed_name" {
			t.Fatalf("%s: preview %+v, want typed %q", c.prev, p, c.expect)
		}
		wrong := mustJSONStr(map[string]string{"yaml": y, "confirm_token": p.ConfirmToken, "confirm_value": "nope"})
		if rr := e.do(t, c.commit, wrong); rr.Code != http.StatusGone {
			t.Fatalf("%s: wrong typed value code %d", c.commit, rr.Code)
		}
		p = decode[pv](t, e.do(t, c.prev, body))
		good := mustJSONStr(map[string]string{"yaml": y, "confirm_token": p.ConfirmToken, "confirm_value": c.expect})
		if rr := e.do(t, c.commit, good); rr.Code != http.StatusOK {
			t.Fatalf("%s: typed commit code %d: %s", c.commit, rr.Code, rr.Body.String())
		}
	}
}

func TestCommitRejectsTokenMintedUnderWeakerPolicy(t *testing.T) {
	strict := false
	pol := func(string) (guardrail.Policy, error) { return guardrail.Policy{RequireTypedConfirm: strict}, nil }
	e := newHardenEnv(t, hardenOpts{policy: pol})
	y := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: default\n"
	p := decode[struct {
		ConfirmToken string `json:"confirm_token"`
	}](t, e.do(t, "/api/v1/apply/preview", mustJSONStr(map[string]string{"yaml": y})))
	strict = true
	rr := e.do(t, "/api/v1/apply/commit", mustJSONStr(map[string]string{"yaml": y, "confirm_token": p.ConfirmToken}))
	if rr.Code != http.StatusGone {
		t.Fatalf("code %d, want 410", rr.Code)
	}
	// Destructive: yes/no token for scale-to-zero minted before strict mode.
	strict = false
	sb := `{"kind":"deploy","name":"web","replicas":0}`
	t1 := decode[confirmationResponse](t, e.do(t, "/api/v1/destructive/scale", sb))
	strict = true
	rr = e.do(t, "/api/v1/destructive/scale", `{"kind":"deploy","name":"web","replicas":0,"confirm_token":"`+t1.Token+`","confirm_value":"web"}`)
	if rr.Code != http.StatusGone {
		t.Fatalf("scale code %d, want 410", rr.Code)
	}
	for _, c := range e.calls() {
		if strings.HasPrefix(c, "apply") || strings.Contains(c, "scale") {
			t.Fatalf("executed: %s", c)
		}
	}
}

func TestPaletteRejectsOverridesAndStreams(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{policy: func(string) (guardrail.Policy, error) {
		return guardrail.Policy{Forbidden: []string{"get secret"}}, nil
	}})
	post := func(argv ...string) *httptest.ResponseRecorder {
		return e.do(t, "/api/v1/passthrough", mustJSONStr(map[string]any{"argv": argv}))
	}
	for name, argv := range map[string][]string{
		"kubeconfig":     {"get", "pods", "--kubeconfig=/x"},
		"context":        {"get", "pods", "--context", "prod"},
		"server":         {"get", "pods", "--server=https://x"},
		"short server":   {"get", "pods", "-shttps://x"},
		"token":          {"get", "pods", "--token=abc"},
		"as":             {"get", "pods", "--as=root"},
		"watch":          {"get", "pods", "-w"},
		"watch long":     {"get", "pods", "--watch"},
		"events watch":   {"events", "--watch"},
		"leading flag":   {"--context=prod", "get", "pods"},
		"forbidden rule": {"get", "secret"},
	} {
		if rr := post(argv...); rr.Code < 400 {
			t.Errorf("%s: code %d, want rejection", name, rr.Code)
		}
	}
	if rr := post("get", "secret"); rr.Code != http.StatusForbidden {
		t.Errorf("forbid rule: code %d, want 403", rr.Code)
	}
	if rr := post("get", "pods", "-n", "kube-system", "-o", "wide"); rr.Code != http.StatusOK {
		t.Errorf("benign read: code %d: %s", rr.Code, rr.Body.String())
	}
	if n := len(e.calls()); n != 1 {
		t.Errorf("kubectl calls = %d, want only the benign one: %v", n, e.calls())
	}
}
