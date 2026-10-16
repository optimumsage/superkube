package web

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func hostStatus(e *hardenEnv, host string) int {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/upgrade/check?token=tok", nil)
	req.Host = host
	rr := httptest.NewRecorder()
	e.s.middleware(e.s.mux).ServeHTTP(rr, req)
	return rr.Code
}

// Item 12: a wildcard bind re-reads interface addresses on a Host miss
// (DHCP renewal, VPN up), rate-limited.
func TestWildcardHostsRefreshOnMiss(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{bind: "0.0.0.0", token: "tok"})
	calls := 0
	addrs := []net.Addr{&net.IPNet{IP: net.ParseIP("10.0.0.5"), Mask: net.CIDRMask(24, 32)}}
	e.s.ifaceAddrs = func() ([]net.Addr, error) { calls++; return addrs, nil }
	e.s.hostsRefreshed = time.Time{}
	e.s.hosts = computeAllowedHosts("0.0.0.0", nil, "")

	if got := hostStatus(e, "10.0.0.5:80"); got != http.StatusOK {
		t.Fatalf("new interface address rejected: %d", got)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	// Another unknown host immediately after: no second interface scan.
	if got := hostStatus(e, "10.9.9.9"); got != http.StatusForbidden {
		t.Fatalf("unknown host: %d", got)
	}
	if got := hostStatus(e, "10.9.9.8"); got != http.StatusForbidden {
		t.Fatalf("unknown host: %d", got)
	}
	if calls != 1 {
		t.Fatalf("refresh not rate limited: %d calls", calls)
	}
}

func TestLoopbackBindNeverRefreshes(t *testing.T) {
	e := newHardenEnv(t, hardenOpts{token: "tok"})
	called := false
	e.s.ifaceAddrs = func() ([]net.Addr, error) { called = true; return nil, nil }
	if got := hostStatus(e, "10.0.0.5"); got != http.StatusForbidden || called {
		t.Fatalf("status %d called=%v", got, called)
	}
}
