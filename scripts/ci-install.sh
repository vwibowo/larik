#!/usr/bin/env bash
# Download a released Larik binary, verify its checksum, and put it on PATH.
# Used by the GitHub Action (action.yml); works on any CI with curl.
#
#   LARIK_VERSION      release tag, e.g. v0.4.0 (default: latest)
#   LARIK_REPO         owner/name of the repository with the releases
#   LARIK_INSTALL_DIR  where to put the binary (default: $RUNNER_TEMP/larik-bin)
#   LARIK_MIN_VERSION  oldest version that will do (default: 0.4.0, the
#                      first with /review)
set -euo pipefail

die() {
  printf 'larik install: %s\n' "$*" >&2
  exit 1
}

version=${LARIK_VERSION:-latest}
repo=${LARIK_REPO:-vwibowo/larik}
min=${LARIK_MIN_VERSION:-0.4.0}
dest=${LARIK_INSTALL_DIR:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}/larik-bin}

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW* | MSYS* | CYGWIN*) os=windows ;;
  *) die "unsupported system: $(uname -s)" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac
binary=larik
archive="larik_${os}_${arch}.tar.gz"
if [[ $os == windows ]]; then
  binary=larik.exe
  archive="larik_${os}_${arch}.zip"
fi

if [[ $version == latest ]]; then
  base="https://github.com/$repo/releases/latest/download"
else
  [[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "version must be latest or a tag like v0.4.0, not $version"
  base="https://github.com/$repo/releases/download/$version"
fi

command -v curl >/dev/null 2>&1 || die 'curl is required'
if command -v sha256sum >/dev/null 2>&1; then
  check() { sha256sum -c -; }
elif command -v shasum >/dev/null 2>&1; then
  check() { shasum -a 256 -c -; }
else
  die 'need sha256sum or shasum to verify the download'
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/larik-install.XXXXXXXX")
trap 'rm -rf "$tmp"' EXIT
printf 'larik install: downloading %s (%s)\n' "$archive" "$version"
curl -fsSL --retry 3 -o "$tmp/$archive" "$base/$archive" || die "could not download $base/$archive"
curl -fsSL --retry 3 -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" || die "could not download $base/SHA256SUMS"

# Check the archive against the release's checksum list before unpacking.
line=$(grep -E "[[:space:]]\*?${archive}\$" "$tmp/SHA256SUMS") || die "$archive is not listed in SHA256SUMS"
(cd "$tmp" && printf '%s\n' "$line" | check >/dev/null) || die "checksum mismatch for $archive"

mkdir -p "$dest"
if [[ $os == windows ]]; then
  unzip -q -o "$tmp/$archive" -d "$dest"
else
  tar -xzf "$tmp/$archive" -C "$dest"
fi
chmod +x "$dest/$binary"

installed=$("$dest/$binary" --version | awk '{print $2}')
# sort -V puts the older version first; the minimum must be it (or equal).
if [[ $(printf '%s\n%s\n' "$min" "$installed" | sort -V | head -n1) != "$min" ]]; then
  die "larik $installed is older than $min, which this needs; set LARIK_VERSION to a newer release"
fi

if [[ -n ${GITHUB_PATH:-} ]]; then
  printf '%s\n' "$dest" >>"$GITHUB_PATH"
fi
printf 'larik install: larik %s in %s\n' "$installed" "$dest"
