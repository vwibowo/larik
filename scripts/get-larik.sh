#!/bin/sh
# Install a released Larik binary: download, verify its checksum, put it on PATH.
#
#   curl -fsSL https://raw.githubusercontent.com/vwibowo/larik/main/scripts/get-larik.sh | sh
#
# The website serves this same file as /install.sh.
#
#   LARIK_VERSION      release tag such as v0.4.0 (default: latest)
#   LARIK_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#   LARIK_REPO         owner/name holding the releases (default: vwibowo/larik)
#   LARIK_BASE_URL     download from here instead of GitHub (for mirrors and tests)
set -eu

die() {
  printf 'larik install: %s\n' "$*" >&2
  exit 1
}

version=${LARIK_VERSION:-latest}
repo=${LARIK_REPO:-vwibowo/larik}
dest=${LARIK_INSTALL_DIR:-$HOME/.local/bin}

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported system: $(uname -s). On Windows, download the zip from the releases page." ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac
archive="larik_${os}_${arch}.tar.gz"

if [ -n "${LARIK_BASE_URL:-}" ]; then
  base=$LARIK_BASE_URL
elif [ "$version" = latest ]; then
  base="https://github.com/$repo/releases/latest/download"
else
  case $version in
    v[0-9]*.[0-9]*.[0-9]*) ;;
    *) die "version must be latest or a tag like v0.4.0, not $version" ;;
  esac
  base="https://github.com/$repo/releases/download/$version"
fi

command -v curl >/dev/null 2>&1 || die 'curl is required'
command -v tar >/dev/null 2>&1 || die 'tar is required'
if command -v sha256sum >/dev/null 2>&1; then
  check() { sha256sum -c -; }
elif command -v shasum >/dev/null 2>&1; then
  check() { shasum -a 256 -c -; }
else
  die 'need sha256sum or shasum to verify the download'
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/larik-install.XXXXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM

printf 'larik install: downloading %s (%s)\n' "$archive" "$version"
curl -fsSL --retry 3 -o "$tmp/$archive" "$base/$archive" || die "could not download $base/$archive"
curl -fsSL --retry 3 -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" || die "could not download $base/SHA256SUMS"

line=$(grep -E "[[:space:]]\*?${archive}\$" "$tmp/SHA256SUMS") || die "$archive is not listed in SHA256SUMS"
(cd "$tmp" && printf '%s\n' "$line" | check >/dev/null 2>&1) || die "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" larik || die "could not unpack $archive"
chmod +x "$tmp/larik"

if mkdir -p "$dest" 2>/dev/null && [ -w "$dest" ]; then
  install -m 0755 "$tmp/larik" "$dest/larik"
else
  command -v sudo >/dev/null 2>&1 || die "$dest is not writable and sudo is unavailable; set LARIK_INSTALL_DIR"
  sudo mkdir -p "$dest"
  sudo install -m 0755 "$tmp/larik" "$dest/larik"
fi

printf 'larik install: installed %s (%s)\n' "$dest/larik" "$("$dest/larik" --version 2>/dev/null || echo unknown)"
case :$PATH: in
  *:"$dest":*) ;;
  *) printf 'larik install: add %s to your PATH, then run: larik\n' "$dest" ;;
esac
