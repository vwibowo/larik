#!/usr/bin/env bash
# Build local release archives. Publishing, tagging, and pushing are manual steps.
set -euo pipefail

die() {
  printf 'release: %s\n' "$*" >&2
  exit 1
}

if [[ $# -ne 1 || ! $1 =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  die 'usage: scripts/package-release.sh vMAJOR.MINOR.PATCH'
fi
version=$1
binary_version=${version#v}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$root"

[[ $(git rev-parse --show-toplevel 2>/dev/null) == "$root" ]] || die 'run from a Git checkout of Larik'
[[ -z $(git status --porcelain --untracked-files=normal) ]] || die 'commit or remove working-tree changes before packaging'
for tool in go git tar zip grep; do
  command -v "$tool" >/dev/null 2>&1 || die "missing required command: $tool"
done
if command -v shasum >/dev/null 2>&1; then
  checksum_tool=shasum
elif command -v sha256sum >/dev/null 2>&1; then
  checksum_tool=sha256sum
else
  die 'need shasum or sha256sum for checksums'
fi

release_root="$root/dist/release"
output="$release_root/$version"
[[ ! -e $output ]] || die "release output already exists: $output"
mkdir -p "$release_root"
stage=$(mktemp -d "$release_root/.${version}.XXXXXXXX")
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/artifacts" "$stage/bin"

printf 'release: testing %s\n' "$version"
go test ./...
printf 'release: checking website links\n'
(cd website && go run . -out "$stage/site")

archives=()
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do
  os=${target%/*}
  arch=${target#*/}
  binary=larik
  extension=tar.gz
  if [[ $os == windows ]]; then
    binary=larik.exe
    extension=zip
  fi
  archive="larik_${os}_${arch}.${extension}"
  grep -Fq "\"file\": \"$archive\"" website/site.json || die "$archive is missing from website/site.json"
  printf 'release: building %s\n' "$target"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w -X main.version=$binary_version" \
    -o "$stage/bin/$binary" ./cmd/larik
  if [[ $os == windows ]]; then
    (cd "$stage/bin" && zip -q "$stage/artifacts/$archive" "$binary")
  else
    tar -czf "$stage/artifacts/$archive" -C "$stage/bin" "$binary"
  fi
  archives+=("$archive")
  rm "$stage/bin/$binary"
done

if [[ $checksum_tool == shasum ]]; then
  (cd "$stage/artifacts" && shasum -a 256 "${archives[@]}" > SHA256SUMS)
else
  (cd "$stage/artifacts" && sha256sum "${archives[@]}" > SHA256SUMS)
fi
mv "$stage/artifacts" "$output"
printf 'release: prepared %s (no tag, push, or GitHub release created)\n' "$output"
