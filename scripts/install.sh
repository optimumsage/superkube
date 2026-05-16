#!/bin/sh
# install.sh — superkube curl-pipe installer.
#
# Downloads the latest GitHub release, verifies the checksum when available,
# and installs `superkube` + `sk` symlink under $PREFIX (default: ~/.local/bin).
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/optimumsage/superkube/main/scripts/install.sh | sh
#   PREFIX=/usr/local/bin curl -fsSL .../install.sh | sh
#   VERSION=v0.2.1 curl -fsSL .../install.sh | sh

set -eu

repo="optimumsage/superkube"
prefix="${PREFIX:-$HOME/.local/bin}"
version="${VERSION:-}"

log() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

# Resolve OS / ARCH using the same naming as .goreleaser.yaml.
case "$(uname -s)" in
    Darwin) os="darwin" ;;
    Linux)  os="linux" ;;
    *) die "unsupported OS: $(uname -s) (only darwin and linux are released)" ;;
esac
case "$(uname -m)" in
    x86_64|amd64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) die "unsupported arch: $(uname -m)" ;;
esac

# Resolve the version: explicit env wins, otherwise ask the GitHub API.
if [ -z "$version" ]; then
    log "resolving latest release..."
    version=$(curl -fsSL "https://api.github.com/repos/${repo}/releases/latest" |
        sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
    [ -n "$version" ] || die "could not resolve latest release tag"
fi
short="${version#v}"
asset="superkube_${short}_${os}_${arch}.tar.gz"
base="https://github.com/${repo}/releases/download/${version}"

log "installing superkube ${version} (${os}/${arch}) to ${prefix}"

# Stage in a temp directory so a failed download never overwrites a working
# install. Always clean up, even on error.
tmpdir=$(mktemp -d 2>/dev/null || mktemp -d -t superkube)
trap 'rm -rf "$tmpdir"' EXIT INT TERM

log "downloading ${asset}..."
curl -fsSL "${base}/${asset}" -o "${tmpdir}/${asset}" ||
    die "download failed: ${base}/${asset}"

# Checksum is best-effort: pre-release / snapshot builds may not publish one.
if curl -fsSL "${base}/checksums.txt" -o "${tmpdir}/checksums.txt" 2>/dev/null; then
    log "verifying checksum..."
    expected=$(grep " ${asset}\$" "${tmpdir}/checksums.txt" | awk '{print $1}')
    if [ -z "$expected" ]; then
        log "warning: ${asset} not in checksums.txt — skipping verification"
    else
        actual=$(shasum -a 256 "${tmpdir}/${asset}" 2>/dev/null | awk '{print $1}')
        [ -n "$actual" ] || actual=$(sha256sum "${tmpdir}/${asset}" | awk '{print $1}')
        [ "$expected" = "$actual" ] ||
            die "checksum mismatch (expected ${expected}, got ${actual})"
    fi
else
    log "warning: no checksums.txt published — skipping verification"
fi

log "extracting..."
tar -xzf "${tmpdir}/${asset}" -C "${tmpdir}"

mkdir -p "$prefix"
install -m 0755 "${tmpdir}/superkube" "${prefix}/superkube" 2>/dev/null ||
    cp "${tmpdir}/superkube" "${prefix}/superkube"
chmod 0755 "${prefix}/superkube"

# Symlink sk → superkube using the helper that ships in the tarball, falling
# back to a plain ln if it's missing (older releases).
if [ -x "${tmpdir}/sk-install-symlink" ]; then
    "${tmpdir}/sk-install-symlink" "${prefix}"
else
    ln -sf "${prefix}/superkube" "${prefix}/sk"
    log "installed ${prefix}/sk -> superkube"
fi

log "installed ${prefix}/superkube"

# Friendly nudge if the install directory isn't on PATH.
case ":${PATH}:" in
    *":${prefix}:"*) ;;
    *) log "" ; log "note: ${prefix} is not on your PATH — add it to your shell rc:"
       log "  export PATH=\"${prefix}:\$PATH\"" ;;
esac

log ""
log "done. try:  superkube version  (or:  sk version)"
