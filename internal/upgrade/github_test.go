package upgrade

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	got := assetName("v0.2.1", "darwin", "arm64")
	want := "superkube_0.2.1_darwin_arm64.tar.gz"
	if got != want {
		t.Errorf("assetName = %q, want %q", got, want)
	}
	// Should also accept tag without "v".
	if got := assetName("0.2.1", "linux", "amd64"); got != "superkube_0.2.1_linux_amd64.tar.gz" {
		t.Errorf("assetName without v: %q", got)
	}
}

func TestAssetURLAndChecksumURL(t *testing.T) {
	a := assetURL("owner/repo", "v1.2.3", "asset.tar.gz")
	if a != "https://github.com/owner/repo/releases/download/v1.2.3/asset.tar.gz" {
		t.Errorf("assetURL = %q", a)
	}
	c := checksumURL("owner/repo", "v1.2.3")
	if c != "https://github.com/owner/repo/releases/download/v1.2.3/checksums.txt" {
		t.Errorf("checksumURL = %q", c)
	}
}

// fakeDoer is a minimal httpDoer that returns the canned response for the
// first matching URL prefix. The test sets routes to whatever the code under
// test should see.
type fakeDoer struct {
	routes  map[string]fakeResponse
	calls   []string
	lastReq *http.Request // captured so tests can assert request headers
}

type fakeResponse struct {
	status int
	body   string
	header http.Header // optional response headers (e.g. rate-limit)
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.calls = append(f.calls, req.URL.String())
	f.lastReq = req
	for prefix, resp := range f.routes {
		if strings.HasPrefix(req.URL.String(), prefix) {
			hdr := resp.header
			if hdr == nil {
				hdr = make(http.Header)
			}
			return &http.Response{
				StatusCode: resp.status,
				Body:       io.NopCloser(strings.NewReader(resp.body)),
				Header:     hdr,
			}, nil
		}
	}
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader("not found")),
		Header:     make(http.Header),
	}, nil
}

func TestLatestRelease(t *testing.T) {
	f := &fakeDoer{routes: map[string]fakeResponse{
		"https://api.github.com/repos/foo/bar/releases/latest": {
			status: 200,
			body:   `{"tag_name": "v9.9.9"}`,
		},
	}}
	got, err := latestRelease(context.Background(), f, "foo/bar")
	if err != nil {
		t.Fatalf("latestRelease err: %v", err)
	}
	if got != "v9.9.9" {
		t.Errorf("latestRelease = %q", got)
	}
}

func TestLatestRelease_NonOK(t *testing.T) {
	f := &fakeDoer{routes: map[string]fakeResponse{
		"https://api.github.com/repos/foo/bar/releases/latest": {status: 503, body: ""},
	}}
	if _, err := latestRelease(context.Background(), f, "foo/bar"); err == nil {
		t.Error("expected error on 503")
	}
}

func TestLatestRelease_MissingTag(t *testing.T) {
	f := &fakeDoer{routes: map[string]fakeResponse{
		"https://api.github.com/repos/foo/bar/releases/latest": {status: 200, body: `{}`},
	}}
	if _, err := latestRelease(context.Background(), f, "foo/bar"); err == nil {
		t.Error("expected error when tag_name absent")
	}
}

func TestLatestReleaseSetsHeaders(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok123")
	t.Setenv("GH_TOKEN", "")
	f := &fakeDoer{routes: map[string]fakeResponse{
		"https://api.github.com/repos/foo/bar/releases/latest": {status: 200, body: `{"tag_name":"v1.0.0"}`},
	}}
	if _, err := latestRelease(context.Background(), f, "foo/bar"); err != nil {
		t.Fatalf("latestRelease err: %v", err)
	}
	if ua := f.lastReq.Header.Get("User-Agent"); !strings.HasPrefix(ua, "superkube/") {
		t.Errorf("User-Agent = %q, want superkube/…", ua)
	}
	if f.lastReq.Header.Get("Accept") == "" {
		t.Error("Accept header not set on API request")
	}
	if got := f.lastReq.Header.Get("Authorization"); got != "Bearer tok123" {
		t.Errorf("Authorization = %q, want Bearer tok123", got)
	}
}

func TestLatestReleaseNoTokenNoAuthHeader(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	f := &fakeDoer{routes: map[string]fakeResponse{
		"https://api.github.com/repos/foo/bar/releases/latest": {status: 200, body: `{"tag_name":"v1.0.0"}`},
	}}
	if _, err := latestRelease(context.Background(), f, "foo/bar"); err != nil {
		t.Fatalf("latestRelease err: %v", err)
	}
	if got := f.lastReq.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization should be absent without a token, got %q", got)
	}
}

func TestLatestReleaseRateLimited(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "0")
	h.Set("X-RateLimit-Reset", "9999999999") // far future → message includes reset
	f := &fakeDoer{routes: map[string]fakeResponse{
		"https://api.github.com/repos/foo/bar/releases/latest": {status: 403, header: h},
	}}
	_, err := latestRelease(context.Background(), f, "foo/bar")
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("expected a rate-limit error, got %v", err)
	}
	if !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Errorf("rate-limit error should hint at GITHUB_TOKEN, got %q", err)
	}
}

func TestLatestRelease403NotRateLimit(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	// A 403 without the rate-limit marker should fall through to the generic
	// message, not be misreported as a rate limit.
	f := &fakeDoer{routes: map[string]fakeResponse{
		"https://api.github.com/repos/foo/bar/releases/latest": {status: 403},
	}}
	_, err := latestRelease(context.Background(), f, "foo/bar")
	if err == nil || strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("plain 403 should not be a rate-limit error, got %v", err)
	}
}

// TestLatestRelease_RealHTTPServer exercises the default client wiring just
// to make sure we don't accidentally bypass it when the caller passes nil.
func TestLatestRelease_RealHTTPServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"tag_name":"v1.0.0"}`)
	}))
	defer srv.Close()

	// We can't easily override the GitHub base URL without complicating the
	// production surface, so the test just ensures the helper client wrapper
	// returns the default when given nil. The latestRelease assertions above
	// already cover the parsing path with fakeDoer.
	if client(nil) != defaultHTTPClient {
		t.Error("client(nil) should return the package default")
	}
}
