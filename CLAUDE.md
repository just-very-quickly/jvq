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
numbers in test/results were recorded again after those fixes. Known and documented: adaptive rounds re-test at the
same level (optional stopping), so a change exactly at the budget is flagged
a little more often than 2.5%.

v0.2.0 (October 2026) works for any language: a `time` adapter (jvq times
the whole command; its clock stops when the command exits, even if a child
keeps the output open), a documented `json` adapter (lines, a document, an
array; log lines and JSON log lines skipped; spelled-out units; BOM handled,
UTF-16 refused), `output` / `-output` for results written to a file, and
`jvq compare OLD NEW` for two saved outputs (go or json). Each run is now
reduced to one value per benchmark and metric, the median of what it
printed, so several values from one run can't narrow the interval. A run
where nothing is gated says why in its note.

The v0.2 review found no statistics errors in the old code; it fixed the
time adapter counting a background child's lifetime, BOMs, `[INFO]` log
lines, pretty JSON after logs, JSON log lines, unknown time units passing
silently, `compare -- -file`, the README's sample config, and a noisy
neighbour that could outlive the scenario harness. Pooling several values
per run was the one design change (now one median per run).

v0.3.0 (October 2026) reads the real harnesses as they are: `criterion`
(cargo bench's time lines, wrapped names, change blocks and colours
skipped, and the bencher format), `google-benchmark` (JSON, repetitions,
skipped entries, NaN counters, aggregates-only), `pytest-benchmark` and
`vitest` (1 to 4) from the files they write via `output`. Testdata in
internal/adapter/testdata is real captured output. A file-size gate:
`size = [patterns]` measured once per side after the build, `[budget] size`
defaulting to 1%, globbed inside the run folder (no absolute or `..`
patterns). Its review fixed criterion's change blocks, colours, Google
Benchmark skips/NaN/aggregates, duplicate names, size globbing, and
compare's choice of format.

Known from the 0.3 recording: pytest-benchmark with a short
`--benchmark-max-time` is noisy on the 2-core VM (1 false alarm in 10
no-change runs); a change exactly at the budget is flagged in 5 or 6 of 50
on a quiet GitHub runner (optional stopping, documented).

v0.4.0 (October 2026), GitLab CI: base detection from GitLab's merge
request variables (merged results and merge trains use head^1 when head is
a merge, else CI_MERGE_REQUEST_TARGET_BRANCH_SHA; detached pipelines use
CI_MERGE_REQUEST_DIFF_BASE_SHA; else the target branch; CI_DEFAULT_BRANCH
as a candidate), `-junit FILE` on run and compare, and ci/gitlab/jvq.yml,
a hidden `.jvq` job to include remotely. Tested with gitlab-ci-local
(installed with npm; needs rsync), not on gitlab.com. Its review caught the
template's tar extraction failing on every run (the archive holds ./jvq),
rebased merge trains, and an unneeded fetch that could kill fork MR jobs.
cmd/jvq/docs_test.go fails until the README, action.yml and the GitLab
template name the version being released.

v0.5.0 (October 2026), history: `jvq run -history FILE` (or `history` in
jvq.toml, `-history -` for none) adds one JSON line per run (changes,
intervals, verdicts; `history_keep` newest, default 1000; a re-run with the
same base and head replaces its line). On the default branch a run starts
from the last line's head when it's an ancestor (base ref `history`), so
pushes of several commits are all measured. `jvq history FILE` chains the
per-run changes (product of 1+change) with an interval: each side's spread
from the run's own interval, added as variances in log space, so one run
gives back its own interval. `-fail-over` counts gated results whose low
end is over the limit. The action has `history`, `history-write`
(auto/true/false), `history-fail-over` (writing runs only) and
`history-key` (default per workflow, job, OS); GitLab has JVQ_HISTORY,
a per-job cache that merge requests only pull. Its review caught noise
adding up to false alarms when chaining point estimates (a real 12-merge
test at +1% each gave -4%), commits skipped between pushes, re-runs
counted twice, pull requests failing on the default branch's creep, and
caches shared between jobs.

v0.5.1 (October 2026), a display fix found while writing the site's case
studies: the summary note for an exact regression printed bytes in
scientific notation ("+6.44254e+06 over the limit of 2.3072e+06"). It now
uses the metric's unit, as the table does ("+6.443 MB over the limit of
2.307 MB", "+104 B over the limit of 0 B"). The formatters moved from
internal/report to internal/units so verdict can use them. No change to
any verdict, so test/results weren't recorded again.

v0.6.0 (October 2026), instruction counts on Linux: an `instructions`
adapter (planned as `measure = "instructions"`; an adapter fitted the code
better) runs each measured run under `valgrind --tool=cachegrind
--cache-sim=no --trace-children=yes`, sums every process's "I refs", and
judges the total like time against `[budget] instructions` (1%, percent
only). No warm-up; GOMAXPROCS=1 unless set (with more Ps, Go's spinning
idle threads made one program count 0.66 G to 23.7 G; at 1, 215.0 to
215.1 M); valgrind's log tail shown on errors. JSON results in the unit
`instructions` are the same metric. Its review caught the GOMAXPROCS
problem, fork-without-exec counting the parent twice (documented), json
instruction units gated without checks, and `jvq check` missing the
budget. test.yml installs valgrind on Linux.

v0.7.0 (October 31 2026), the flaky test gate: `jvq flaky [flags]
RESULTS...` reads one CI run's JUnit XML and `go test -json` results
(files or folders; shards and mixed formats are one run; Surefire's
flaky/rerun elements, -count and gotestsum reruns as attempts; a hung or
panicking Go test failed; a package failing with no failing test is
`pkg (package)`), adds the run to a test history (JSON lines,
`{"kind":"tests",...,"commit":...,"tests":{"id":"fp"}}`, newest 200
kept), and replays the whole history each time. A flaky commit is one
where a test passed and failed; 2 of them in a test's last 100 runs put it
in quarantine; 30 passing runs in a row take it out; 3 failed attempts in
a row make it broken (out, blocking). A failure blocks unless the test is
in quarantine after this run. Flags: -history, -commit (GITHUB_SHA,
CI_COMMIT_SHA, git HEAD), -no-write, -json, -markdown, -q; `[flaky]` in
jvq.toml; `jvq check` shows it. Code in internal/testresults,
internal/flaky, cmd/jvq/flaky.go; the accuracy workflow ignores those
paths. test/flaky simulates CI histories through the binary into
test/results/flaky.*: 200 stable + 5 flaky tests over 300 commits x 10,
red first runs 53.7% without quarantine vs 14.6% with (9.6% in the last
100 commits), 0 stable tests quarantined; a broken quarantined test
blocked within 3 runs without retries (50/50) and in its first run with 2
retries (50/50, at the cost of 6 false "broken" blocks in 529 runs); a
fixed test released after 30 passes (10/10); damaged and deleted
histories gave no errors. No subagent could be spawned for the review, so
it was a second pass over the diff; it fixed non-result JSON in a results
folder failing the run (now skipped with a note), a broken event on a
flaky row, a -no-write note, a per-test map in the replay, and Windows
file-mode checks in the tests. Known limits: without retries, a
quarantined test that really broke gets through up to 2 runs (listed as a
quarantined failure); PR runs with -no-write don't add evidence; the
history repeats test names (2000 long Go names x 200 runs = 27 MB); no
action.yml input yet (the README shows actions/cache steps). Maven
Central was unreachable, so testdata/surefire-reruns.xml is hand-written
to Surefire's schema.

Recording notes for 0.7.0: this VM was shared with other sessions' builds
(load 5 to 8 on 2 cores) while recording, so timing tests flaked:
TestCommandAfterDashes (now verdict-agnostic: it tests the command after
--, and also flaked once on the macOS runner), TestTimeAdapterTimesTheCommand
under -race, and TestInstructionsSteadyWithGoroutines under -race
(inconclusive at +0.4%). test/results/race.txt is the honest record of
the last of those; tests.txt (no race) passed. The accuracy suite and
scenarios weren't re-recorded (no change to the benchmark gate).

Not done yet: submodules in worktrees.

## In progress: flaky case studies

Started October 31 2026 (no change to the gate's code; only new files under
test/results/case-studies/flaky/ and this file):
1. Retitle the site's 0.7.0 post without the dot ("jvq Quarantines Flaky
   Tests So They Stop Blocking Merges") and fix links to its old URL.
2. Two or three case studies from real test runs: clone real open source
   projects with known flaky tests, run their suites several times on one
   commit (go test -json, pytest --junitxml), feed the runs to the released
   v0.7.0 binary, record commands, commits, trimmed results and jvq output
   with a rerun script under test/results/case-studies/flaky/.
3. A use examples post (GitHub Actions cache, gotestsum reruns, pytest
   JUnit, reading the quarantine, releasing a fixed test, why 2 retries),
   each recipe run for real.
4. Link the posts from the 0.7.0 post and the gate page's case studies.
5. A short done entry here.

## Test results

`sh scripts/record.sh` runs every test, the race detector, the release plan
cases, the smoke check on a static build, and the accuracy suite
(`test/accuracy`: 200 no-change runs, 50 runs each at +2/5/10/20/50% work,
100 runs each for one extra allocation and its control), and writes them to
`test/results/`, then the scenarios (`test/scenarios`: a noisy neighbour
taking half the CPUs and churning 512 MB, drift of 2% per run in ABBA and
sequential order through the runner directly, and a Python script through
the time and json adapters, and the real harnesses end to end: pytest-benchmark,
Google Benchmark's Python bindings, vitest and criterion, 10 runs each,
skipped and listed when a tool is missing, and the instructions adapter,
20 runs each of no change, +0.5%, +2%, both beside the neighbour, and a Go
program with 8 allocating goroutines) into scenarios.json and
scenarios.md. The scenarios take over an hour on the VM, so run them as their
own background job (`go run ./test/scenarios -out test/results`) rather than
inside record.sh, whose background run may hit a 2-hour limit. Every
number in the README and on the site comes from those files; record again
after any change that could move them. The accuracy suite takes 20 to 40
minutes and the scenarios about as long again; `SKIP_ACCURACY=1` skips both.
The flaky gate's simulation (test/flaky, about 10 minutes, flaky.json and
flaky.md) runs too unless `SKIP_FLAKY=1`.
Don't run other heavy jobs on the machine while it records, not even
`go test ./...`: a concurrent test run once disturbed a recording, and
another run's stale-worktree cleanup removed a test's workspace. Raise the
version before recording a release, so the files name the right version.

The `accuracy` workflow records the same suite on a GitHub-hosted runner when
the deciding code changes (or by hand) and pushes it to the
`accuracy-results` branch; record.sh copies it into `test/results/github/`. The first one (October 2026,
commit e4a746f) showed 0.4% noise, 0/200 false alarms, +10% caught 50/50. The
one in test/results/github now (commit df1205a, 0.6.0) has the scenarios
too: 0.5% noise, 0/200 false alarms, +10% caught 50/50, +5% flagged 2/50,
every instruction-count case right in 5 rounds (valgrind is installed by
the workflow), and every other scenario case settled correctly except
drift in ABBA order, which is inconclusive.

The `benchmarks` workflow runs the released action on jvq's own benchmarks
(jvq.toml at the root) on pull requests and after each release.

## Site

JVQ.net is in `photozeitgeist/jvq.net`, rebuilt in October 2026 from the old
blog (wiped at the owner's request) with the Hugo Launch template copied
unchanged from taskfused.com: content and config values only. Pages: about,
contact, quick start, download, reference. Posts: the launch, performance
budgets in CI explained, how jvq was tested, the name, then for 0.2 the
release post and the harder-settings post (scenarios), then a release
post for each of 0.3, 0.4, 0.5, 0.6 and 0.7 (the flaky test gate). The site also hosts jvqq, the
query tool (repo just-very-quickly/query), with its own pages and case
studies. Images are drawn into
static/images. Every number comes from `test/results/`; when a recording
changes a number, update the site too.

## Next

Possible next steps for the flaky gate, for the owner to decide: an
action.yml input (or a small companion action) that installs jvq and keeps
the test history in the cache; a pinned list in `[flaky]` to quarantine or
never quarantine tests by name; a more compact history for big suites.


Approved with the plan (October 2026), in this order:

- **v0.2.0, any language.** Released October 2026 (see above).
- **v0.3.0, more harnesses.** Released October 2026 (see above).

Approved by the owner after v0.3.0 (October 19 2026, "all three"), each its
own release with a review, a fresh recording and site posts:

- **v0.4.0, GitLab CI.** Released October 2026 (see above). A ready `.gitlab-ci.yml` example, base detection
  from GitLab's merge request variables, and a JUnit report (`-junit`) that
  GitLab (and Jenkins) show in the merge request.
- **v0.5.0, history.** Released October 2026 (see above). An optional history file (`-history`), one line per
  run, kept between CI runs as a cache or artifact, and `jvq history` to
  show each benchmark's trend.
- **v0.6.0, instruction counts on Linux.** Released October 2026 (see
  above). `measure = "instructions"`: count
  the instructions the command runs instead of timing it, nearly free of
  noise. CI machines (this VM, GitHub's runners) don't expose hardware
  counters, so it runs the command under valgrind's cachegrind, which
  works without them (checked October 2026: a Go program's count repeats to
  within a few in ten million).
