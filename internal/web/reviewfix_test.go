package web

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Item 7: a stream opened via newSSE (logs, watch, and the POST-driven AI
// endpoints) outlives the server-wide read/write timeouts.
func TestSSEOutlivesServerTimeouts(t *testing.T) {
	oldR, oldW := serverReadTimeout, serverWriteTimeout
	serverReadTimeout, serverWriteTimeout = 300*time.Millisecond, 300*time.Millisecond
	defer func() { serverReadTimeout, serverWriteTimeout = oldR, oldW }()

	e := newHardenEnv(t, hardenOpts{})
	h := e.s.logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the (small) request body first, as the AI handlers do.
		_, _ = io.Copy(io.Discard, r.Body)
		sse, err := newSSE(w)
		if err != nil {
			t.Error(err)
			return
		}
		for i := 0; i < 8; i++ {
			if err := sse.Send("tick", i); err != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}))
	srv := newHTTPServer(h)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	resp, err := http.Post("http://"+ln.Addr().String()+"/", "application/json", strings.NewReader(`{"q":1}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if n := strings.Count(string(b), "event: tick"); n != 8 {
		t.Fatalf("stream cut short: %d/8 events", n)
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
