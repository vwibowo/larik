#!/usr/bin/env bash
# Build Larik and install it into a system-wide or custom bin directory.
set -euo pipefail

die() {
  printf 'install: %s\n' "$*" >&2
  exit 1
}

root=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
prefix=${PREFIX:-/usr/local}
bindir=${BINDIR:-$prefix/bin}

command -v go >/dev/null 2>&1 || die 'Go is required; install Go and try again'
command -v install >/dev/null 2>&1 || die 'the install command is required'

mkdir -p "$bindir" 2>/dev/null || {
  [[ $bindir == /usr/* || $bindir == /opt/* ]] || die "cannot create $bindir"
  command -v sudo >/dev/null 2>&1 || die "cannot create $bindir and sudo is unavailable"
  sudo mkdir -p "$bindir"
}

tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/larik-install.XXXXXXXX")
trap 'rm -rf "$tmpdir"' EXIT
binary="$tmpdir/larik"

printf 'install: building Larik\n'
(cd "$root" && go build -trimpath -o "$binary" ./cmd/larik)

if [[ -w $bindir ]]; then
  install -m 0755 "$binary" "$bindir/larik"
else
  command -v sudo >/dev/null 2>&1 || die "$bindir is not writable and sudo is unavailable"
  sudo install -m 0755 "$binary" "$bindir/larik"
fi

printf 'install: installed %s\n' "$bindir/larik"
if [[ :$PATH: != *:"$bindir":* ]]; then
  printf 'install: add %s to PATH if it is not already there\n' "$bindir"
fi
