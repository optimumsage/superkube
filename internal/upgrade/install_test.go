package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseChecksum(t *testing.T) {
	body := `abc123  superkube_0.2.0_darwin_amd64.tar.gz
deadbeef *superkube_0.2.0_darwin_arm64.tar.gz
cafe1234  superkube_0.2.0_linux_amd64.tar.gz
`
	cases := map[string]string{
		"superkube_0.2.0_darwin_amd64.tar.gz": "abc123",
		"superkube_0.2.0_darwin_arm64.tar.gz": "deadbeef",
		"superkube_0.2.0_linux_amd64.tar.gz":  "cafe1234",
		"not_in_list.tar.gz":                  "",
	}
	for asset, want := range cases {
		got, err := parseChecksum(strings.NewReader(body), asset)
		if err != nil {
			t.Fatalf("parse err: %v", err)
		}
		if got != want {
			t.Errorf("parseChecksum(%q) = %q, want %q", asset, got, want)
		}
	}
}

// TestExtractAndReplace builds a fake tarball, extracts the binary, and
// atomically replaces a stand-in "current" binary. This exercises the full
// post-download pipeline without any network calls.
func TestExtractAndReplace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("upgrade is unix-only")
	}
	dir := t.TempDir()

	// Build a fake .tar.gz containing a "superkube" file.
	tarPath := filepath.Join(dir, "release.tar.gz")
	wantContent := []byte("#!/fake binary contents")
	writeFakeTarball(t, tarPath, "superkube", wantContent)

	// Extract.
	extracted := filepath.Join(dir, "superkube.new")
	if err := extractBinary(tarPath, "superkube", extracted); err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	got, err := os.ReadFile(extracted)
	if err != nil {
		t.Fatalf("read extracted: %v", err)
	}
	if !bytes.Equal(got, wantContent) {
		t.Errorf("extracted content mismatch: got %q want %q", got, wantContent)
	}
	info, err := os.Stat(extracted)
	if err != nil {
		t.Fatalf("stat extracted: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("extracted binary is not executable")
	}

	// Stage a stand-in "current" binary and replace it.
	current := filepath.Join(dir, "current_superkube")
	if err := os.WriteFile(current, []byte("OLD"), 0o755); err != nil {
		t.Fatalf("write current: %v", err)
	}
	if err := replaceBinary(current, extracted); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	after, err := os.ReadFile(current)
	if err != nil {
		t.Fatalf("read replaced: %v", err)
	}
	if !bytes.Equal(after, wantContent) {
		t.Errorf("after replace: got %q, want %q", after, wantContent)
	}
}

func TestExtractBinary_MissingEntry(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "release.tar.gz")
	writeFakeTarball(t, tarPath, "something-else", []byte("nope"))
	err := extractBinary(tarPath, "superkube", filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("expected error when binary absent from archive")
	}
}

func TestVerifyAgainstChecksumsURL(t *testing.T) {
	dir := t.TempDir()
	// Create a file with known sha256.
	data := []byte("hello upgrade")
	asset := filepath.Join(dir, "asset.tar.gz")
	if err := os.WriteFile(asset, data, 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	body := hexSum + "  asset.tar.gz\n"

	t.Run("match", func(t *testing.T) {
		f := &fakeDoer{routes: map[string]fakeResponse{
			"https://example/checksums.txt": {status: 200, body: body},
		}}
		err := verifyAgainstChecksumsURL(t.Context(), f, "https://example/checksums.txt", asset, "asset.tar.gz")
		if err != nil {
			t.Errorf("verify err: %v", err)
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		bad := strings.Replace(body, hexSum, strings.Repeat("0", 64), 1)
		f := &fakeDoer{routes: map[string]fakeResponse{
			"https://example/checksums.txt": {status: 200, body: bad},
		}}
		err := verifyAgainstChecksumsURL(t.Context(), f, "https://example/checksums.txt", asset, "asset.tar.gz")
		if err == nil || strings.Contains(err.Error(), "no checksums") {
			t.Errorf("expected mismatch error, got %v", err)
		}
	})

	t.Run("404 returns errNoChecksumPublished", func(t *testing.T) {
		f := &fakeDoer{routes: map[string]fakeResponse{
			"https://example/checksums.txt": {status: 404, body: ""},
		}}
		err := verifyAgainstChecksumsURL(t.Context(), f, "https://example/checksums.txt", asset, "asset.tar.gz")
		if !errors.Is(err, errNoChecksumPublished) {
			t.Errorf("expected errNoChecksumPublished, got %v", err)
		}
	})

	t.Run("asset absent from list", func(t *testing.T) {
		f := &fakeDoer{routes: map[string]fakeResponse{
			"https://example/checksums.txt": {status: 200, body: "deadbeef  other.tar.gz\n"},
		}}
		err := verifyAgainstChecksumsURL(t.Context(), f, "https://example/checksums.txt", asset, "asset.tar.gz")
		if !errors.Is(err, errNoChecksumPublished) {
			t.Errorf("expected errNoChecksumPublished, got %v", err)
		}
	})
}

// writeFakeTarball emits a gzip-compressed tar archive with one regular file.
// Used to drive extractBinary without depending on a real release.
func writeFakeTarball(t *testing.T, path, name string, body []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tarball: %v", err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o755,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write hdr: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
}
