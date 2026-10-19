# Releasing jvq

Releases go out on their own. Nobody clicks Publish. This file is the record of
how that works, for whoever (person or Claude session) works on this repo next.

## The short version

1. Raise `const Version = "X.Y.Z"` in `cmd/jvq/version.go`.
   Change the version the examples name to match: the action's in README.md
   and action.yml, and `JVQ_VERSION` and the include URL in
   ci/gitlab/jvq.yml and README.md. A test (cmd/jvq/docs_test.go) fails
   until they do, so the release can't go out pointing at the last one.
2. Write what changed into the commit message.
3. Push the commit to `main`.

When the `test` workflow passes on that commit (the full suite and the smoke
check on Linux, macOS and Windows), the `release` workflow builds the binaries,
smoke-tests them, then tags the commit `vX.Y.Z` and publishes the release
"jvq vX.Y.Z" with the archives and SHA256SUMS attached. If any build or check
fails, nothing is tagged and nothing goes public. Fix it, push again, and the
next green run picks it up.

Pushing without raising the version is fine: the release workflow sees the tag
already exists and stops quietly.

## What it does, step by step

[.github/workflows/release.yml](.github/workflows/release.yml) starts when a
`test` run finishes; the push alone doesn't start it.

- **plan.** Goes on only if the `test` run passed, came from a push to `main` in
  this repo, and `main` still points at that commit (if `main` moved on, the run
  for the newer commit decides). Reads `Version` from `cmd/jvq/version.go`.
  Stops quietly if `vX.Y.Z` is already a tag. Fails loudly if the version is
  lower than the newest tag. [scripts/release-plan.py](scripts/release-plan.py)
  has the rules, and [scripts/release_plan_test.py](scripts/release_plan_test.py)
  its cases.
- **build.** jvq is pure Go, so one Linux job cross-builds all six binaries
  without cgo (`-trimpath -ldflags '-s -w'`), checks each one's architecture
  with `file` and that the Linux ones are static, and packages them.
- **check.** Runs [scripts/smoke.py](scripts/smoke.py) on each binary its
  runner can run: Linux x86-64, Linux ARM64, macOS Apple silicon and Windows
  x86-64, each on its own kind of machine. The macOS Intel and Windows ARM64
  builds are checked for their architecture only.
- **release.** Runs only after the build and every check passed. Writes
  SHA256SUMS and the notes, then creates the tag and the release in one step
  with `gh release create --target <commit>`, on the repo's own `GITHUB_TOKEN`
  (`contents: write` on this job only). A `concurrency` group per tag means two
  runs for the same version can't publish twice.

The smoke check, on each binary: `jvq version` names the version in the source;
`jvq check` reads a jvq.toml; on a fresh git repository with a Go benchmark, a
head doing three times the work per op is a regression (exit 1, JSON verdict
regression), a head that changes only a comment passes (exit 0), and a missing
base is an error (exit 2); and no worktrees are left behind.

## Files that go out

| System | File |
|---|---|
| Linux, x86-64 (static) | jvq_linux_amd64.tar.gz |
| Linux, ARM64 (static) | jvq_linux_arm64.tar.gz |
| macOS, Apple silicon | jvq_darwin_arm64.tar.gz |
| macOS, Intel | jvq_darwin_amd64.tar.gz |
| Windows, x86-64 | jvq_windows_amd64.zip |
| Windows, ARM64 | jvq_windows_arm64.zip |
| Checksums | SHA256SUMS |

Each archive holds `jvq` (or `jvq.exe`), LICENSE, NOTICE,
THIRD_PARTY_LICENSES.md and README.md. Archive names carry no version on
purpose: links like
`https://github.com/just-very-quickly/jvq/releases/latest/download/jvq_linux_amd64.tar.gz`
keep working from one release to the next, so the README and the site never need
editing for a release. The GitHub Action (action.yml) downloads the archive for
the tag it was referenced by and checks it against SHA256SUMS.

## After a release

A Claude session can't create tags or releases itself, which is why the workflow
does it on the repository's token. After pushing a raised version, check:

1. The `test` run and then the `release` run finished green. The repository is
   public, so `https://api.github.com/repos/just-very-quickly/jvq/actions/runs`
   shows them without a token.
2. The release exists with all seven files
   (`https://api.github.com/repos/just-very-quickly/jvq/releases/latest`).
3. The `latest/download` links resolve, and a downloaded archive's SHA256 matches
   SHA256SUMS.
4. A downloaded Linux binary runs: `jvq version` names the release.
5. The `benchmarks` workflow, which runs after each release, used the action and
   the new binary on this repository's own benchmarks.

## By hand

- A release published by hand gets the binaries attached by the same workflow.
- To rebuild the files of an existing tag, run the `release` workflow by hand
  with the tag.
