#!/usr/bin/env bash
# Write the license and NOTICE texts of every module compiled into the release
# binaries, plus Go's own, to THIRD_PARTY_NOTICES.txt (or the given path).
set -euo pipefail

# Keep notice ordering identical across release machines and locales.
export LC_ALL=C

die() {
  printf 'notices: %s\n' "$*" >&2
  exit 1
}

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$root"
out=${1:-$root/THIRD_PARTY_NOTICES.txt}

# Dependencies differ by platform, so take the union over the release targets.
modules=$(
  for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do
    CGO_ENABLED=0 GOOS=${target%/*} GOARCH=${target#*/} go list -deps \
      -f '{{if not .Standard}}{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}{{end}}' \
      ./cmd/larik
  done | sort -u
)
[[ -n $modules ]] || die 'go list found no modules'

# Homebrew moves Go's LICENSE out of GOROOT, one directory up.
goroot=$(go env GOROOT)
go_license=$goroot/LICENSE
[[ -f $go_license ]] || go_license=$goroot/../LICENSE
[[ -f $go_license ]] || die "no LICENSE file in or next to $goroot"

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
{
  printf 'Larik is built from the third-party software listed below. Each entry\n'
  printf 'reproduces that software'\''s license and, where it has one, its NOTICE file.\n'
  printf '\n================ Go %s (golang.org) ================\n\n' "$(go env GOVERSION)"
  cat "$go_license"
  while read -r path version dir; do
    [[ -d $dir ]] || die "$path $version is not in the module cache; run go mod download"
    license=$(find "$dir" -maxdepth 1 -type f \( -iname 'licen[cs]e*' -o -iname 'copying*' \) | sort | head -1)
    [[ -n $license ]] || die "$path $version has no license file"
    printf '\n================ %s %s ================\n\n' "$path" "$version"
    cat "$license"
    find "$dir" -maxdepth 1 -type f -iname 'notice*' | sort | while read -r notice; do
      printf '\n---- NOTICE ----\n\n'
      cat "$notice"
    done
  done <<< "$modules"
} > "$tmp"
mv "$tmp" "$out"
trap - EXIT
printf 'notices: wrote %s modules to %s\n' "$(wc -l <<< "$modules" | tr -d ' ')" "$out"
