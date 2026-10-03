---
name: github-release
description: Prepare and optionally publish a Larik GitHub release by deriving the next semantic version, comparing changes since the previous release, updating the changelog and release metadata, packaging all supported binaries, and verifying the assets.
argument-hint: "[major|minor|patch]"
---

# Larik GitHub release

Use this skill when preparing a release for this repository. `$ARGUMENTS` may be
`major`, `minor`, or `patch`; if omitted, inspect the changes and recommend the
appropriate bump, but do not guess when the choice is ambiguous.

## Safety boundary

- Read `AGENTS.md` before doing anything. Its repository-specific rules win.
- Never expose API keys, tokens, signing keys, or other secrets in release notes,
  command output, commits, or GitHub metadata.
- Do not add a remote, push, create a tag, publish a GitHub Release, or run a
  destructive Git command unless the user explicitly asks for that specific
  remote action in the current conversation. Preparing and verifying local
  release artifacts is allowed without that confirmation.
- Before any remote action, show the exact commands, tag, branch, repository,
  release title, and assets, then ask for confirmation. A request to prepare a
  release is not by itself permission to publish it.
- Do not modify unrelated user changes. If the working tree is dirty, inspect
  each change and either preserve it or stop and ask how to proceed; never hide,
  reset, or discard it automatically.

## Release workflow

### 1. Establish the version baseline

Run these read-only checks first:

```bash
git status --short
git tag --list 'v[0-9]*' --sort=-version:refname
git log -1 --format='%H%n%s%n%ad' --date=iso-strict
rg 'var version|site.json|Unreleased|v[0-9]+\.[0-9]+\.[0-9]+' cmd/larik/main.go website/site.json website/content/changelog.md docs/contributing-guide.md
```

Use the newest valid semver tag as the previous release. If no valid tag exists,
use the version in `cmd/larik/main.go` and clearly say that the comparison has no
release tag baseline. Do not trust a stale hard-coded version in documentation
when a tag or release metadata gives a newer value.

Select the next version:

- `major`: incompatible public behavior or API changes;
- `minor`: new backwards-compatible user-facing capabilities;
- `patch`: fixes, documentation, packaging, and backwards-compatible polish.

The final version must be greater than the previous release. Use the `vMAJOR.MINOR.PATCH`
form for tags and release directories, and the bare `MAJOR.MINOR.PATCH` form in
`cmd/larik/main.go` and embedded binary version output.

### 2. Build the change summary from the previous release

Compare the complete range, not only the latest commit:

```bash
git log --no-merges --format='%h %s' PREVIOUS_TAG..HEAD
git diff --stat PREVIOUS_TAG..HEAD
git diff --name-status PREVIOUS_TAG..HEAD
```

Read the affected files as needed and group user-visible changes into concise
release-note bullets: features, fixes, performance, compatibility, documentation,
and known limitations. Exclude internal implementation details unless they
matter to users. Never copy credentials or private paths into the notes.

Merge the summary into the existing `## Unreleased` section of
`website/content/changelog.md`. Preserve older Unreleased notes that still
belong to this release, remove duplicates, and do not rewrite historical release
sections. Add the release date only when the release is actually being finalized.

### 3. Update release metadata consistently

For the selected version, update all of these together:

- `cmd/larik/main.go`: `var version` to the bare version;
- `website/content/changelog.md`: rename the finalized Unreleased heading to
  `## vX.Y.Z`, add the publication date, and keep the generated release notes;
- `website/site.json`: set the current version and every supported download URL
  to the same `vX.Y.Z` tag and archive names;
- `docs/contributing-guide.md`: update examples that describe the next planned
  release, but do not claim publication before it happens;
- release notes, if requested, must use the same version and the same change
  summary as the changelog.

Keep website download URLs pointed at the last published release until the new
GitHub assets actually exist. If the release is only being prepared locally,
do not make the website advertise unavailable assets; leave a clearly described
pending state in the local notes instead.

### 4. Verify locally

Before committing release metadata:

```bash
gofmt -w <changed Go files>
go test ./...
go build -o larik ./cmd/larik
(cd website && go run .)
git diff --check
graphify update .
```

Then ensure the version is embedded correctly:

```bash
./larik --version
```

The output must contain the new bare version. Review the diff and confirm no
unrelated files or secrets are included.

### 5. Package and verify assets

Only after the checkout is clean and the release metadata is committed should
the local package script be run:

```bash
scripts/package-release.sh vX.Y.Z
```

The script runs the test suite and website link checks, then creates archives in
`dist/release/vX.Y.Z/` for:

- `darwin/arm64`
- `darwin/amd64`
- `linux/arm64`
- `linux/amd64`
- `windows/amd64`

Verify that all five archives and `SHA256SUMS` exist, validate the checksums,
and inspect at least the macOS arm64 archive's `--version` output. Do not add
`dist/release` artifacts to the source commit unless the user explicitly asks.

### 6. Commit and publish only with confirmation

Create a focused release commit containing the version, changelog, website
metadata, and required documentation. Use a message such as:

```text
release: prepare vX.Y.Z
```

Before publishing, present a checklist containing:

- previous tag and new tag;
- the generated change summary;
- exact commit and tag to publish;
- GitHub repository resolved from `origin`;
- asset paths and checksums;
- whether `website/site.json` currently points at published or pending assets.

Only after explicit confirmation for the remote actions may you run commands like:

```bash
git tag -a vX.Y.Z -m "Larik vX.Y.Z" COMMIT
git push origin BRANCH vX.Y.Z
gh release create vX.Y.Z dist/release/vX.Y.Z/* \
  --verify-tag --title "Larik vX.Y.Z" --notes-file RELEASE_NOTES.md
```

Replace `BRANCH`, repository details, and notes paths after inspecting them. Never
assume `main`, never force-push, and never publish before all assets and checksums
are verified. After publishing, verify the GitHub release asset list and then
update the website metadata to the published URLs in a separate documented commit
if the project workflow requires it.

## Final report

Report:

- previous version/tag and new version/tag;
- why that semver bump was selected;
- the changelog sections and release metadata changed;
- tests, website generation, packaging, checksums, and binary-version checks;
- commit hash;
- whether tagging, pushing, and GitHub publishing were performed or deliberately
  left for explicit confirmation.
