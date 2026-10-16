package audit

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	rotateThreshold = 10 * 1024 * 1024 // 10 MB
	keepArchives    = 14
)

// MaybeRotate checks the live audit log's size and, if over the threshold,
// renames it to audit-YYYYMMDD-HHMMSS.log.gz (gzipped) and trims older
// archives to keepArchives. Called opportunistically before each append.
// Best-effort; any failure leaves the live file in place.
func MaybeRotate() {
	path := LogPath()
	info, err := os.Stat(path)
	if err != nil || info.Size() < rotateThreshold {
		return
	}

	dir := filepath.Dir(path)
	stamp := time.Now().UTC().Format("20060102-150405")
	rotated := filepath.Join(dir, "audit-"+stamp+".log")

	// Atomic rename within the same directory.
	if err := os.Rename(path, rotated); err != nil {
		return
	}
	// Compress in foreground (audit log rotation is rare; the cost is bounded).
	if err := gzipFile(rotated); err == nil {
		_ = os.Remove(rotated)
	}
	pruneArchives(dir, keepArchives)
}

// gzipFile compresses path to path+".gz". The archive is only considered
// valid once the gzip stream is closed and the file is synced and closed; on
// any failure the partial archive is removed so the caller keeps the source.
func gzipFile(path string) (err error) {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	dst := path + ".gz"
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = out.Close()
			_ = os.Remove(dst)
		}
	}()
	gz := gzip.NewWriter(out)
	if _, err = io.Copy(gz, in); err != nil {
		_ = gz.Close()
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	return out.Close()
}

func pruneArchives(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type archive struct {
		name string
		mod  time.Time
	}
	var archives []archive
	for _, e := range entries {
		n := e.Name()
		if len(n) < len("audit-") || n[:6] != "audit-" || filepath.Ext(n) != ".gz" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		archives = append(archives, archive{name: n, mod: info.ModTime()})
	}
	if len(archives) <= keep {
		return
	}
	sort.Slice(archives, func(i, j int) bool { return archives[i].mod.After(archives[j].mod) })
	for _, a := range archives[keep:] {
		_ = os.Remove(filepath.Join(dir, a.name))
	}
}
