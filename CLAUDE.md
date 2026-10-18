# Notes for Claude

This file says where the next session starts and what comes next. What comes
next is decided with the owner before it goes here, and work starts only when
the owner says go. There is no separate roadmap file.

jvq runs on the owner's low maintenance workflow: Claude does the code, tests,
docs, CI, releases, site and upkeep; the owner gives the go-ahead. The code is
Apache 2.0, the README is the manual, docs/ holds the reference, and there are
no Word or PDF documents.

## Releases

Releases are automated. To release, raise `Version` in `cmd/jvq/version.go`
and push to `main`; never ask the owner to publish one.
[RELEASING.md](RELEASING.md) has the procedure and the checks to run
afterwards. This session's `gh` has no valid token, but the repository is
public: read runs and releases from `https://api.github.com/repos/just-very-quickly/jvq/...`.

## Where things stand

v0.1.0 is the first release (October 2026): a performance budget gate for CI
in Go, standard library only. `jvq run` checks out base and head into git
worktrees, runs the benchmark command in ABBA rounds with a warm-up, judges
time with the Hodges-Lehmann shift in log time and its exact Moses interval
(regression only when the whole interval is over budget), adds rounds while
undecided (10 to start, up to 30), gates bytes and allocations per op on
medians, and reports to the terminal, Markdown (and the GitHub job summary)
and JSON. Exit codes: 0 pass, 1 regression, 2 error. A GitHub Action
(action.yml) downloads the release binary and checks its SHA256.
[docs/reference.md](docs/reference.md) is the full reference.

Changes from the approved plan, made while building v0.1.0:
- Adaptive rounds moved from v0.2 into v0.1: on a noisy machine (about 7%
  run-to-run) 10 rounds can't settle a 5% budget, so without it the gate was
  mostly "inconclusive".
- The interval is the exact rank-based (Moses) one rather than a bootstrap:
  same idea, no randomness, checked against published tables.
- No golang.org/x/perf dependency: the Go benchmark parser and a small TOML
  subset reader are in the repo, so there are no third-party modules.

An independent review before the release found no errors in the statistics
and fixed: the default command on Windows (`-run=NONE`, since cmd.exe eats
`^`), [budget.bench] tables now applied in file order, jvq.toml found through
symlinked folders, stale-worktree cleanup limited to this host's dead runs,
the GOMAXPROCS suffix stripped only when every line shares it, and a
background child holding output no longer failing a good run. The accuracy
numbers were recorded just before those fixes; none of them touch how
verdicts are reached. Known and documented: adaptive rounds re-test at the
same level (optional stopping), so a change exactly at the budget is flagged
a little more often than 2.5%.

Not done yet: other benchmark formats, `jvq compare`, submodules in worktrees.

## Test results

`sh scripts/record.sh` runs every test, the race detector, the release plan
cases, the smoke check on a static build, and the accuracy suite
(`test/accuracy`: 200 no-change runs, 50 runs each at +2/5/10/20/50% work,
100 runs each for one extra allocation and its control), and writes them to
`test/results/`. Every number in the README and on the site comes from those
files; record again after any change that could move them. The accuracy suite
takes 20 to 40 minutes; `SKIP_ACCURACY=1` skips it. Don't run other heavy jobs
on the machine while it records.

The `accuracy` workflow records the same suite on a GitHub-hosted runner when
the deciding code changes (or by hand) and pushes it to the
`accuracy-results` branch; record.sh copies it into `test/results/github/`.

The `benchmarks` workflow runs the released action on jvq's own benchmarks
(jvq.toml at the root) on pull requests and after each release.

## Site

JVQ.net is in `photozeitgeist/jvq.net`, rebuilt in October 2026 from the old
blog (wiped at the owner's request) with the Hugo Launch template copied
unchanged from taskfused.com: content and config values only. Pages: about,
contact, quick start, download, reference. Posts: the launch, performance
budgets in CI explained, how jvq was tested, the name. Images are drawn into
static/images. Every number comes from `test/results/`; when a recording
changes a number, update the site too.

## Next

Approved with the plan (October 2026), in this order:

- **v0.2.0, any language.** A `time` adapter (jvq times the whole command
  itself) and a small documented `json` adapter; `jvq compare old.json
  new.json`; recorded tests for a noisy neighbour (a CPU and memory hog
  running alongside), for drift (ABBA against a sequential order, using the
  runner's test-only Sequential mode), and for non-Go commands.
- **v0.3.0, more harnesses.** Adapters for Rust criterion, Google Benchmark
  JSON, pytest-benchmark and vitest bench; a binary-size gate.

After v0.3.0, only with the owner's approval: an instruction-count mode on
Linux, a GitLab CI example, an optional history file kept as a CI artifact.
