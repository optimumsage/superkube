#!/bin/sh
# bump-version.sh — bump the Version constant in internal/version/version.go.
#
# Usage:
#   scripts/bump-version.sh patch              # 0.2.0 -> 0.2.1
#   scripts/bump-version.sh minor              # 0.2.0 -> 0.3.0
#   scripts/bump-version.sh major              # 0.2.0 -> 1.0.0
#   scripts/bump-version.sh 1.2.3              # set explicit version
#   scripts/bump-version.sh minor --commit     # also create a chore commit
#   scripts/bump-version.sh minor --commit --tag  # commit and create vX.Y.Z tag

set -eu

repo_root=$(cd "$(dirname "$0")/.." && pwd)
file="$repo_root/internal/version/version.go"

die() { printf 'bump-version.sh: %s\n' "$*" >&2; exit 1; }
usage() {
    sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'
    exit "${1:-2}"
}

[ $# -ge 1 ] || usage
[ -f "$file" ] || die "version file not found: $file"

bump="$1"; shift
do_commit=0
do_tag=0
for arg in "$@"; do
    case "$arg" in
        --commit) do_commit=1 ;;
        --tag)    do_tag=1; do_commit=1 ;;
        -h|--help) usage 0 ;;
        *) die "unknown option: $arg" ;;
    esac
done

current=$(awk -F'"' '/^[[:space:]]*Version[[:space:]]*=/ { print $2; exit }' "$file")
[ -n "$current" ] || die "could not read current Version from $file"

case "$current" in
    *.*.*) ;;
    *) die "current version is not semver-shaped: $current" ;;
esac

major=$(printf '%s\n' "$current" | cut -d. -f1)
minor=$(printf '%s\n' "$current" | cut -d. -f2)
patch=$(printf '%s\n' "$current" | cut -d. -f3)

case "$bump" in
    major) new="$((major + 1)).0.0" ;;
    minor) new="$major.$((minor + 1)).0" ;;
    patch) new="$major.$minor.$((patch + 1))" ;;
    [0-9]*.[0-9]*.[0-9]*) new="$bump" ;;
    *) die "expected patch|minor|major|X.Y.Z, got: $bump" ;;
esac

if [ "$new" = "$current" ]; then
    die "new version equals current ($current); nothing to do"
fi

# In-place rewrite. Use a tmp file to stay portable across BSD/GNU sed.
tmp="$file.tmp.$$"
awk -v new="$new" '
    /^[[:space:]]*Version[[:space:]]*=/ && !done {
        sub(/"[^"]*"/, "\"" new "\"")
        done = 1
    }
    { print }
' "$file" > "$tmp"
mv "$tmp" "$file"

printf 'bumped %s -> %s (%s)\n' "$current" "$new" "$file"

if [ "$do_commit" -eq 1 ]; then
    command -v git >/dev/null 2>&1 || die "--commit requested but git is not on PATH"
    cd "$repo_root"
    git add internal/version/version.go
    git commit -m "chore: bump version to $new"
    printf 'committed: chore: bump version to %s\n' "$new"
fi

if [ "$do_tag" -eq 1 ]; then
    tag="v$new"
    if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
        die "tag $tag already exists"
    fi
    git tag -a "$tag" -m "$tag"
    printf 'tagged: %s\n' "$tag"
fi
