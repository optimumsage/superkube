package upgrade

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// errNoChecksumPublished is sentinel: the release exists but no checksums.txt
// was uploaded. We treat this the same way install.sh does — warn and
// continue. Wrapped errors are still surfaced as hard failures.
var errNoChecksumPublished = errors.New("no checksums.txt published")

// download streams a URL into dest. It does NOT buffer the whole body in
// memory — release tarballs are small but a writer-by-writer copy keeps memory
// flat regardless of asset size.
func download(ctx context.Context, h httpDoer, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client(h).Do(req)
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// verifyAgainstChecksumsURL fetches the checksums.txt for the release and
// compares the entry for assetName against the sha256 of file. Returns
// errNoChecksumPublished if the checksum file itself is absent (release just
// missing the artifact) so callers can downgrade to a warning.
func verifyAgainstChecksumsURL(ctx context.Context, h httpDoer, url, file, assetName string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client(h).Do(req)
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return errNoChecksumPublished
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch checksums: %s", resp.Status)
	}
	expected, err := parseChecksum(resp.Body, assetName)
	if err != nil {
		return err
	}
	if expected == "" {
		// File present but doesn't list our asset. install.sh treats this as
		// a warning, not an error.
		return errNoChecksumPublished
	}
	actual, err := sha256File(file)
	if err != nil {
		return err
	}
	if expected != actual {
		return fmt.Errorf("checksum mismatch for %s (expected %s, got %s)", assetName, expected, actual)
	}
	return nil
}

// parseChecksum reads a `sha256sum`-formatted text stream and returns the
// hex digest associated with assetName, or "" if absent. Lines look like:
//
//	<64-hex>  superkube_0.2.0_darwin_arm64.tar.gz
//
// We tolerate "*" binary-mode prefixes and arbitrary whitespace between cols.
func parseChecksum(r io.Reader, assetName string) (string, error) {
	s := bufio.NewScanner(r)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == assetName {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", s.Err()
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractBinary pulls a single file named `entryName` out of a gzipped tar
// archive and writes it to dest with 0755 perms. We avoid extracting the whole
// archive because the only file we care about is the binary; LICENSE and
// README are noise here.
func extractBinary(tarballPath, entryName, dest string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip open: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}
		// Match on basename so a leading "./" or directory prefix doesn't
		// trip us up — goreleaser archives historically lay files at the
		// archive root, but defensively normalize.
		if filepath.Base(hdr.Name) != entryName {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("binary %q not found in archive", entryName)
}

// replaceBinary atomically swaps newBin into currentPath. The strategy:
//  1. Rename(newBin, currentPath) — atomic when both paths share a filesystem.
//
// On POSIX an `os.Rename` over a running binary works because the kernel
// tracks the file by inode: existing processes keep their inode mapping,
// while new opens see the replacement. We deliberately do NOT delete-then-
// write, since a failed write would leave the user with no binary.
//
// To get same-filesystem atomicity we move newBin next to currentPath first.
// If permissions block us, surface a clean error so the CLI can suggest sudo.
func replaceBinary(currentPath, newBin string) error {
	dir := filepath.Dir(currentPath)
	staged := filepath.Join(dir, "."+filepath.Base(currentPath)+".upgrade")

	// Copy (not rename) from temp dir into the target directory. Crossing
	// filesystems would make the subsequent rename non-atomic; even within
	// the same FS the temp dir is usually elsewhere, so copy first.
	if err := copyFile(newBin, staged, 0o755); err != nil {
		if isPermErr(err) {
			return permissionHint(currentPath, err)
		}
		return err
	}

	// Match the target's existing mode bits (executable + whatever else was
	// set), so we don't accidentally widen permissions on a tightly locked-
	// down install.
	if info, err := os.Stat(currentPath); err == nil {
		_ = os.Chmod(staged, info.Mode().Perm()|0o111)
	}

	if err := os.Rename(staged, currentPath); err != nil {
		_ = os.Remove(staged)
		if isPermErr(err) {
			return permissionHint(currentPath, err)
		}
		return err
	}
	return nil
}

// copyFile is the cross-filesystem-safe equivalent of os.Rename for the
// download step. It preserves mode bits via the caller; we don't read the
// source's mode because the staged temp file's perms are not load-bearing.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

func isPermErr(err error) bool {
	return errors.Is(err, os.ErrPermission)
}

// permissionHint wraps a permission error with a suggestion the CLI can show
// to the user. We don't shell out to sudo ourselves — that'd be surprising —
// but pointing the user at the right command saves a round trip.
func permissionHint(path string, err error) error {
	return fmt.Errorf("permission denied writing %s: %w\nhint: re-run with sudo, or reinstall via PREFIX=$HOME/.local/bin install.sh", path, err)
}
