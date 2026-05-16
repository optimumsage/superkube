// Package upgrade implements `sk upgrade`: it resolves the latest release on
// GitHub, downloads the platform-matched tarball, verifies it against the
// published checksum, and atomically replaces the currently running binary.
//
// The HTTP/release/install pipeline deliberately mirrors scripts/install.sh so
// curl-pipe installs and `sk upgrade` end up at the same binary on disk.
package upgrade

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// Repo is the GitHub repo we pull releases from. Centralized so tests and
// future flags can override it in one place.
const Repo = "optimumsage/superkube"

// ErrUpToDate signals that the installed version already matches (or is newer
// than) the requested target. Callers handle it as a clean exit with a friendly
// message — it is not an error condition.
var ErrUpToDate = errors.New("already up to date")

// Plan describes a single upgrade operation: where the new binary will come
// from and which on-disk file it will overwrite. Building the plan is cheap
// and network-bound; executing it does I/O. Splitting the two lets callers
// preview (`--check`) without committing to anything.
type Plan struct {
	CurrentVersion string // e.g. "0.2.0"; "" if unknown
	TargetVersion  string // e.g. "v0.2.1" — always with the leading "v"
	OS             string // "darwin" | "linux"
	Arch           string // "amd64" | "arm64"
	AssetName      string // e.g. "superkube_0.2.1_darwin_arm64.tar.gz"
	AssetURL       string
	ChecksumURL    string
	BinaryPath     string // absolute path to the binary we'll replace
}

// Options configures CheckLatest. Tests and the CLI both build one of these
// rather than threading individual params through.
type Options struct {
	// CurrentVersion is the running binary's version string (with or without
	// "v"). May be empty for dev builds — Plan treats that as "always upgrade".
	CurrentVersion string

	// TargetVersion pins a specific release. Empty means "latest". Accepts
	// "v0.2.1" or "0.2.1".
	TargetVersion string

	// Force skips the "already up to date" check. The download still runs.
	Force bool

	// OS / Arch override runtime.GOOS / runtime.GOARCH. For tests only; in
	// production these default to the current platform.
	OS, Arch string

	// HTTPClient overrides the default client. Tests inject a fake; nil means
	// use the package default.
	HTTPClient httpDoer

	// Repo overrides the default repo. For tests.
	Repo string
}

func (o Options) repo() string {
	if o.Repo != "" {
		return o.Repo
	}
	return Repo
}

func (o Options) os() string {
	if o.OS != "" {
		return o.OS
	}
	return runtime.GOOS
}

func (o Options) arch() string {
	if o.Arch != "" {
		return o.Arch
	}
	return runtime.GOARCH
}

// CheckLatest resolves the target version against GitHub and returns the Plan
// the caller should execute. Returns ErrUpToDate when the running binary is
// already at or ahead of the target (unless Force is set).
func CheckLatest(ctx context.Context, opt Options) (*Plan, error) {
	if err := supportedPlatform(opt.os(), opt.arch()); err != nil {
		return nil, err
	}

	target := opt.TargetVersion
	if target == "" {
		latest, err := latestRelease(ctx, opt.HTTPClient, opt.repo())
		if err != nil {
			return nil, fmt.Errorf("resolve latest release: %w", err)
		}
		target = latest
	}
	target = normalizeTag(target)

	if !opt.Force && opt.CurrentVersion != "" {
		cmp, err := compareSemver(stripV(opt.CurrentVersion), stripV(target))
		if err == nil && cmp >= 0 {
			return nil, fmt.Errorf("%w: current=%s target=%s", ErrUpToDate, opt.CurrentVersion, target)
		}
	}

	binPath, err := resolveBinaryPath()
	if err != nil {
		return nil, fmt.Errorf("resolve binary path: %w", err)
	}

	asset := assetName(target, opt.os(), opt.arch())
	return &Plan{
		CurrentVersion: opt.CurrentVersion,
		TargetVersion:  target,
		OS:             opt.os(),
		Arch:           opt.arch(),
		AssetName:      asset,
		AssetURL:       assetURL(opt.repo(), target, asset),
		ChecksumURL:    checksumURL(opt.repo(), target),
		BinaryPath:     binPath,
	}, nil
}

// Run downloads, verifies, and installs the binary described by Plan. The
// running binary at plan.BinaryPath is atomically swapped — concurrent
// invocations keep their inode, so an already-running `sk` is unaffected.
//
// progress, if non-nil, receives a one-line status update at each phase
// ("downloading…", "verifying…", "installing…"). The CLI wires this to a
// spinner; tests pass nil.
func Run(ctx context.Context, p *Plan, opt Options, progress func(string)) error {
	if p == nil {
		return errors.New("nil plan")
	}
	if progress == nil {
		progress = func(string) {}
	}

	tmpdir, err := os.MkdirTemp("", "superkube-upgrade-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpdir)

	progress("downloading " + p.AssetName)
	tarPath := filepath.Join(tmpdir, p.AssetName)
	if err := download(ctx, opt.HTTPClient, p.AssetURL, tarPath); err != nil {
		return fmt.Errorf("download %s: %w", p.AssetURL, err)
	}

	progress("verifying checksum")
	if err := verifyAgainstChecksumsURL(ctx, opt.HTTPClient, p.ChecksumURL, tarPath, p.AssetName); err != nil {
		// Snapshot / pre-release builds may not publish checksums. Mirror
		// install.sh's "best-effort" behavior: warn, but continue.
		if !errors.Is(err, errNoChecksumPublished) {
			return fmt.Errorf("checksum verification: %w", err)
		}
		progress("warning: no checksums.txt published — skipping verification")
	}

	progress("extracting")
	newBin := filepath.Join(tmpdir, "superkube.new")
	if err := extractBinary(tarPath, "superkube", newBin); err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}

	progress("installing to " + p.BinaryPath)
	if err := replaceBinary(p.BinaryPath, newBin); err != nil {
		return fmt.Errorf("install %s: %w", p.BinaryPath, err)
	}
	return nil
}

// resolveBinaryPath returns the absolute, symlink-resolved path of the running
// executable. We want to overwrite the *real* file, not a symlink to it — on
// many setups `sk` is a symlink to `superkube` in the same directory.
func resolveBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// Symlink resolution can fail on weird filesystems; fall back to the
		// raw path. Better to attempt the install than refuse outright.
		resolved = exe
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return resolved, nil
	}
	return abs, nil
}

func supportedPlatform(os, arch string) error {
	switch os {
	case "darwin", "linux":
	default:
		return fmt.Errorf("unsupported OS %q: only darwin and linux are released", os)
	}
	switch arch {
	case "amd64", "arm64":
	default:
		return fmt.Errorf("unsupported arch %q: only amd64 and arm64 are released", arch)
	}
	return nil
}

// drainAndClose is a small helper so callers don't litter the package with
// `io.Copy(io.Discard, ...)` boilerplate when they need to make sure an HTTP
// body is exhausted before close (keep-alive reuse).
func drainAndClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()
}
