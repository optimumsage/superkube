package audit

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestGzipFile_RoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit-x.log")
	if err := os.WriteFile(p, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := gzipFile(p); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p + ".gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	if string(b) != "hello\n" {
		t.Errorf("got %q", b)
	}
}

func TestGzipFile_FailureLeavesNoArchive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "missing.log")
	if err := gzipFile(p); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(p + ".gz"); err == nil {
		t.Error("partial archive left behind")
	}
}
