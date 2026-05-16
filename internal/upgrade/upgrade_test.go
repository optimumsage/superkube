package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckLatest_UpToDate(t *testing.T) {
	f := &fakeDoer{routes: map[string]fakeResponse{
		"https://api.github.com/repos/foo/bar/releases/latest": {
			status: 200,
			body:   `{"tag_name":"v0.2.0"}`,
		},
	}}
	_, err := CheckLatest(context.Background(), Options{
		CurrentVersion: "0.2.0",
		OS:             "darwin",
		Arch:           "arm64",
		HTTPClient:     f,
		Repo:           "foo/bar",
	})
	if !errors.Is(err, ErrUpToDate) {
		t.Errorf("want ErrUpToDate, got %v", err)
	}
}

func TestCheckLatest_NewerAvailable(t *testing.T) {
	f := &fakeDoer{routes: map[string]fakeResponse{
		"https://api.github.com/repos/foo/bar/releases/latest": {
			status: 200,
			body:   `{"tag_name":"v0.3.0"}`,
		},
	}}
	plan, err := CheckLatest(context.Background(), Options{
		CurrentVersion: "0.2.0",
		OS:             "linux",
		Arch:           "amd64",
		HTTPClient:     f,
		Repo:           "foo/bar",
	})
	if err != nil {
		t.Fatalf("CheckLatest err: %v", err)
	}
	if plan.TargetVersion != "v0.3.0" {
		t.Errorf("TargetVersion = %q", plan.TargetVersion)
	}
	if plan.AssetName != "superkube_0.3.0_linux_amd64.tar.gz" {
		t.Errorf("AssetName = %q", plan.AssetName)
	}
	if plan.AssetURL != "https://github.com/foo/bar/releases/download/v0.3.0/superkube_0.3.0_linux_amd64.tar.gz" {
		t.Errorf("AssetURL = %q", plan.AssetURL)
	}
	if plan.BinaryPath == "" {
		t.Error("BinaryPath empty")
	}
}

func TestCheckLatest_ForceBypassesUpToDate(t *testing.T) {
	f := &fakeDoer{routes: map[string]fakeResponse{
		"https://api.github.com/repos/foo/bar/releases/latest": {
			status: 200,
			body:   `{"tag_name":"v0.2.0"}`,
		},
	}}
	plan, err := CheckLatest(context.Background(), Options{
		CurrentVersion: "0.2.0",
		Force:          true,
		OS:             "darwin",
		Arch:           "arm64",
		HTTPClient:     f,
		Repo:           "foo/bar",
	})
	if err != nil {
		t.Fatalf("Force should bypass ErrUpToDate: %v", err)
	}
	if plan.TargetVersion != "v0.2.0" {
		t.Errorf("TargetVersion = %q", plan.TargetVersion)
	}
}

func TestCheckLatest_PinnedVersion(t *testing.T) {
	// No API call should happen when --version is set; latest endpoint
	// returns 500 to confirm we don't touch it.
	f := &fakeDoer{routes: map[string]fakeResponse{
		"https://api.github.com/": {status: 500, body: "should not be called"},
	}}
	plan, err := CheckLatest(context.Background(), Options{
		CurrentVersion: "0.2.0",
		TargetVersion:  "v0.5.0",
		OS:             "darwin",
		Arch:           "arm64",
		HTTPClient:     f,
		Repo:           "foo/bar",
	})
	if err != nil {
		t.Fatalf("CheckLatest err: %v", err)
	}
	if plan.TargetVersion != "v0.5.0" {
		t.Errorf("TargetVersion = %q", plan.TargetVersion)
	}
}

func TestCheckLatest_PinnedVersion_AcceptsNoV(t *testing.T) {
	f := &fakeDoer{}
	plan, err := CheckLatest(context.Background(), Options{
		CurrentVersion: "0.2.0",
		TargetVersion:  "0.5.0",
		OS:             "linux",
		Arch:           "arm64",
		HTTPClient:     f,
		Repo:           "foo/bar",
	})
	if err != nil {
		t.Fatalf("CheckLatest err: %v", err)
	}
	if plan.TargetVersion != "v0.5.0" {
		t.Errorf("expected v-prefixed tag, got %q", plan.TargetVersion)
	}
}

func TestCheckLatest_UnsupportedPlatform(t *testing.T) {
	cases := []struct{ os, arch string }{
		{"windows", "amd64"},
		{"darwin", "386"},
		{"plan9", "arm64"},
	}
	for _, tc := range cases {
		_, err := CheckLatest(context.Background(), Options{
			CurrentVersion: "0.2.0",
			OS:             tc.os,
			Arch:           tc.arch,
			HTTPClient:     &fakeDoer{},
			Repo:           "foo/bar",
		})
		if err == nil {
			t.Errorf("expected error for %s/%s", tc.os, tc.arch)
		}
	}
}

func TestCheckLatest_EmptyCurrentVersionAlwaysProceeds(t *testing.T) {
	// Dev builds may have empty/unknown version. Don't refuse — always offer
	// the upgrade.
	f := &fakeDoer{routes: map[string]fakeResponse{
		"https://api.github.com/repos/foo/bar/releases/latest": {
			status: 200,
			body:   `{"tag_name":"v0.2.0"}`,
		},
	}}
	_, err := CheckLatest(context.Background(), Options{
		CurrentVersion: "",
		OS:             "darwin",
		Arch:           "arm64",
		HTTPClient:     f,
		Repo:           "foo/bar",
	})
	if err != nil {
		t.Errorf("empty current version should proceed: %v", err)
	}
}

// TestRun_EndToEnd_NoChecksum exercises Run with a fake httpDoer that serves
// a synthetic tarball and a 404 for checksums.txt. Verifies the missing
// checksum is treated as a warning, the binary lands on disk, and progress
// callbacks fire.
func TestRun_EndToEnd_NoChecksum(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "superkube")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}

	wantContent := []byte("new binary v0.3.0")
	tarBytes := buildTarball(t, "superkube", wantContent)

	f := &routingDoer{routes: map[string]routedResponse{
		"https://github.com/foo/bar/releases/download/v0.3.0/superkube_0.3.0_linux_amd64.tar.gz": {
			status: 200, body: tarBytes,
		},
		"https://github.com/foo/bar/releases/download/v0.3.0/checksums.txt": {
			status: 404,
		},
	}}

	plan := &Plan{
		CurrentVersion: "0.2.0",
		TargetVersion:  "v0.3.0",
		OS:             "linux",
		Arch:           "amd64",
		AssetName:      "superkube_0.3.0_linux_amd64.tar.gz",
		AssetURL:       "https://github.com/foo/bar/releases/download/v0.3.0/superkube_0.3.0_linux_amd64.tar.gz",
		ChecksumURL:    "https://github.com/foo/bar/releases/download/v0.3.0/checksums.txt",
		BinaryPath:     target,
	}

	var progressLog []string
	err := Run(context.Background(), plan, Options{HTTPClient: f}, func(s string) {
		progressLog = append(progressLog, s)
	})
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read replaced: %v", err)
	}
	if !bytes.Equal(got, wantContent) {
		t.Errorf("target content mismatch: got %q, want %q", got, wantContent)
	}
	if len(progressLog) == 0 {
		t.Error("expected at least one progress message")
	}
}

func TestRun_EndToEnd_WithChecksumMatch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "superkube")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}

	tarBytes := buildTarball(t, "superkube", []byte("v0.3.0 contents"))
	sum := sha256.Sum256(tarBytes)
	checksumsBody := hex.EncodeToString(sum[:]) + "  superkube_0.3.0_linux_amd64.tar.gz\n"

	f := &routingDoer{routes: map[string]routedResponse{
		"https://github.com/foo/bar/releases/download/v0.3.0/superkube_0.3.0_linux_amd64.tar.gz": {
			status: 200, body: tarBytes,
		},
		"https://github.com/foo/bar/releases/download/v0.3.0/checksums.txt": {
			status: 200, body: []byte(checksumsBody),
		},
	}}

	plan := &Plan{
		TargetVersion: "v0.3.0",
		AssetName:     "superkube_0.3.0_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/foo/bar/releases/download/v0.3.0/superkube_0.3.0_linux_amd64.tar.gz",
		ChecksumURL:   "https://github.com/foo/bar/releases/download/v0.3.0/checksums.txt",
		BinaryPath:    target,
	}
	if err := Run(context.Background(), plan, Options{HTTPClient: f}, nil); err != nil {
		t.Fatalf("Run err: %v", err)
	}
}

func TestRun_ChecksumMismatchAborts(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "superkube")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}

	tarBytes := buildTarball(t, "superkube", []byte("contents"))
	checksumsBody := strings.Repeat("0", 64) + "  superkube_0.3.0_linux_amd64.tar.gz\n"

	f := &routingDoer{routes: map[string]routedResponse{
		"https://github.com/foo/bar/releases/download/v0.3.0/superkube_0.3.0_linux_amd64.tar.gz": {
			status: 200, body: tarBytes,
		},
		"https://github.com/foo/bar/releases/download/v0.3.0/checksums.txt": {
			status: 200, body: []byte(checksumsBody),
		},
	}}

	plan := &Plan{
		TargetVersion: "v0.3.0",
		AssetName:     "superkube_0.3.0_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/foo/bar/releases/download/v0.3.0/superkube_0.3.0_linux_amd64.tar.gz",
		ChecksumURL:   "https://github.com/foo/bar/releases/download/v0.3.0/checksums.txt",
		BinaryPath:    target,
	}
	err := Run(context.Background(), plan, Options{HTTPClient: f}, nil)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	// Original binary must still be intact.
	got, _ := os.ReadFile(target)
	if string(got) != "OLD" {
		t.Errorf("target was clobbered despite checksum failure: got %q", got)
	}
}

func TestRun_DownloadFailureLeavesBinaryIntact(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "superkube")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	f := &routingDoer{routes: map[string]routedResponse{
		"https://github.com/foo/bar/releases/download/v0.3.0/superkube_0.3.0_linux_amd64.tar.gz": {
			status: 500,
		},
	}}
	plan := &Plan{
		TargetVersion: "v0.3.0",
		AssetName:     "superkube_0.3.0_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/foo/bar/releases/download/v0.3.0/superkube_0.3.0_linux_amd64.tar.gz",
		ChecksumURL:   "https://github.com/foo/bar/releases/download/v0.3.0/checksums.txt",
		BinaryPath:    target,
	}
	if err := Run(context.Background(), plan, Options{HTTPClient: f}, nil); err == nil {
		t.Fatal("expected download error")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "OLD" {
		t.Errorf("target should be untouched on failure, got %q", got)
	}
}

func TestRun_NilPlan(t *testing.T) {
	if err := Run(context.Background(), nil, Options{}, nil); err == nil {
		t.Error("expected error on nil plan")
	}
}

// routingDoer is a bytes-friendly variant of fakeDoer used by end-to-end
// tests that need to serve binary tarballs.
type routingDoer struct {
	routes map[string]routedResponse
}

type routedResponse struct {
	status int
	body   []byte
}

func (r *routingDoer) Do(req *http.Request) (*http.Response, error) {
	if resp, ok := r.routes[req.URL.String()]; ok {
		body := resp.body
		return &http.Response{
			StatusCode: resp.status,
			Body:       readCloser(body),
			Header:     make(http.Header),
		}, nil
	}
	return &http.Response{
		StatusCode: 404,
		Body:       readCloser(nil),
		Header:     make(http.Header),
	}, nil
}

func readCloser(b []byte) interface {
	Read(p []byte) (int, error)
	Close() error
} {
	return &byteCloser{r: bytes.NewReader(b)}
}

type byteCloser struct{ r *bytes.Reader }

func (b *byteCloser) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *byteCloser) Close() error               { return nil }

func buildTarball(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ar.tar.gz")
	writeFakeTarball(t, path, name, body)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tarball: %v", err)
	}
	return data
}
