# jvq reference

Everything jvq v0.7 does, in one place. The [README](../README.md) is the
introduction.

## Commands

### jvq run

```
jvq run [flags] [-- command]
```

Compares two commits of the repository that holds the current folder and
exits with the verdict.

What happens, in order:

1. Settings are read: built-in defaults, then `jvq.toml` (the `-config`
   file, or the first `jvq.toml` found in the current folder or a parent up
   to the repository root), then flags. A command after `--` replaces the
   config's `command`. A single word after `--` is used as written (so
   `jvq run -- "go test -bench=. ./..."` works); several words are quoted
   for the shell and joined. With no command anywhere, and a `go.mod` in the
   current folder, the command is `go test -run=NONE -bench=. -benchmem ./...`.
2. Worktree folders left behind by a jvq run that was killed (its process is
   gone) are removed, and git's records of them pruned.
3. Head is resolved (`-head`, default `HEAD`). Base is `-base`, or else:
   - in a GitHub pull request run, the merge base of head and
     `origin/$GITHUB_BASE_REF`;
   - in a GitLab merge request pipeline (`CI_MERGE_REQUEST_IID` set): in a
     merged results pipeline or merge train
     (`CI_MERGE_REQUEST_EVENT_TYPE` is `merged_result` or `merge_train`),
     head's first parent when head is a merge commit (the target branch
     GitLab merged into), or else `CI_MERGE_REQUEST_TARGET_BRANCH_SHA`
     (fast-forward and semi-linear projects rebase instead of merging);
     otherwise `CI_MERGE_REQUEST_DIFF_BASE_SHA`; failing both, the merge base with
     `origin/$CI_MERGE_REQUEST_TARGET_BRANCH_NAME`. A commit GitLab names
     that isn't in the clone is an error saying to set `GIT_DEPTH: "0"` and
     fetch the target branch;
   - otherwise the merge base of head and the first of: origin's default
     branch (`refs/remotes/origin/HEAD`), `origin/$CI_DEFAULT_BRANCH` and
     `$CI_DEFAULT_BRANCH` when GitLab sets it, `origin/main`,
     `origin/master`, `main`, `master`;
   - if that merge base is head itself (head is on the base branch, as on a
     push to main), head's first parent.
   A shallow clone that lacks the commits needed is an error that says to
   fetch the history (`fetch-depth: 0` in `actions/checkout`).
4. Base and head are checked out, detached, into `base/` and `head/` in a
   new temp folder named `jvq-run-*`. The command runs in the same
   subfolder of each worktree as the current folder is in the repository.
   Submodules stay empty unless `submodules = true` (or `-submodules`)
   is set; then `git submodule update --init --recursive` checks them out
   in both worktrees, at the commits each side records. Without it, a
   repository with a `.gitmodules` file gets a note.
5. If `build` is set, it runs once in each worktree, base first.
6. If `warmup` is on, the command runs once on each side and the results
   are thrown away (but must parse).
7. `rounds` rounds run in ABBA order: round 1 base then head, round 2 head
   then base, and so on.
8. Every benchmark and metric is judged (see Verdicts). While a time result
   is inconclusive because its interval straddles the budget (or has too
   few samples for an interval), two more rounds run, continuing the ABBA
   order, up to `max_rounds` per side.
9. The report is printed, written to `-json` and `-markdown` files if
   given, and appended to `$GITHUB_STEP_SUMMARY` if set. In GitHub Actions
   each regression and any error also becomes an error annotation.
10. The worktrees and the temp folder are removed (unless `-keep`), also
    after an error or an interrupt (Ctrl-C or SIGTERM).

Each command run gets `JVQ_SIDE` (`base` or `head`) and `JVQ_ROUND` (0 for
the build and warm-up, then 1, 2, ...) in its environment. It runs with
`sh -c` on Linux and macOS and `cmd.exe /S /C` on Windows, in its own
process group, so a timeout or an interrupt stops everything it started.
Only its standard output is parsed (or the `output` file, if set); both
outputs are shown if it fails. With `output` set, that file (relative to
where the command runs, or absolute) is deleted before each run and read
after it, so a file left by an earlier run can't pass for a new one; a run
that doesn't write it is an error.

| Flag | Default | Meaning |
|---|---|---|
| `-base REF` | see above | the commit to compare against |
| `-head REF` | `HEAD` | the commit to check |
| `-config FILE` | found | config file |
| `-rounds N` | 10 | rounds per side to start with, at least 3 |
| `-max-rounds N` | 30 | most rounds per side while undecided; at or below `-rounds` turns adding rounds off |
| `-build CMD` | none | command run once per side before the rounds |
| `-adapter NAME` | `go` | how to read results: `go`, `json`, `time`, `instructions`, `criterion`, `google-benchmark`, `pytest-benchmark` or `vitest` |
| `-output FILE` | none | read results from this file, which the command writes, instead of its standard output |
| `-timeout D` | `10m` | limit for each command run (Go duration: `90s`, `10m`) |
| `-json FILE` | none | also write the report as JSON |
| `-markdown FILE` | none | also write the report as Markdown |
| `-junit FILE` | none | also write the report as JUnit XML |
| `-history FILE` | `history` in jvq.toml | add a line for this run to a history file |
| `-base-may-fail` | off | if base fails to build or run, print a note and exit 0 with nothing compared |
| `-fail-on-inconclusive` | off | exit 1 when the overall verdict is inconclusive |
| `-no-warmup` | off | skip the warm-up runs |
| `-keep` | off | leave the worktrees in place and print where |
| `-submodules` | `submodules` in jvq.toml | check out submodules in both worktrees |
| `-q` | off | no progress lines on stderr |

### jvq compare

```
jvq compare [flags] OLD NEW
```

Judges two saved benchmark outputs, such as `go test -bench=. -count=10`
before and after a change, with the same statistics, budgets and exit codes
as `jvq run`. OLD plays base, NEW plays head. Flags may come before or after
the file names.

| Flag | Default | Meaning |
|---|---|---|
| `-adapter NAME` | see below | any adapter but `time` |
| `-config FILE` | found | config file, for budgets, ignores, confidence and `fail_on_inconclusive` |
| `-json FILE` | none | also write the report as JSON |
| `-markdown FILE` | none | also write the report as Markdown |
| `-fail-on-inconclusive` | off | exit 1 when the overall verdict is inconclusive |

The format is `-adapter`, or the `adapter` set in a jvq.toml (unless it's
`time`), or `json` when both files end in `.json` or `.jsonl`, or `go`.
The `time` adapter can't be used: it times a command as it runs. Each file
needs at least 3 samples of a benchmark for a time verdict (4 a side for an
interval at 95%); with Go output that means `-count`, with Google Benchmark
`--benchmark_repetitions`, and with criterion several `cargo bench` outputs
in one file. pytest-benchmark and vitest give one value per benchmark per
file, which compare can't judge. A file with no results
is an error. Unlike `jvq run`, the two outputs weren't interleaved, so a
machine that changed between them looks like a change in the code. The
report says so in its note and has `"mode": "compare"`, the file names as
the base and head `ref`, and no commit hashes or rounds.

### jvq history

```
jvq history [flags] FILE
```

Reads a history file and prints, for each benchmark and metric, how many
runs measured it, its change in all with an interval, how many runs called
it a regression, and its change run by run for the latest 30. It also
says how many times a run's base wasn't the previous run's head (commits
in between that no run measured).

| Flag | Default | Meaning |
|---|---|---|
| `-n N` | all | only the newest N runs |
| `-bench PATTERN` | all | only benchmarks matching the pattern |
| `-metric NAME` | all | only this metric |
| `-fail-over PCT` | none | exit 1 if a gated result has moved more than this in all, beyond its noise (see below) |
| `-markdown FILE` | none | also write the table as Markdown |

It also appends the Markdown table to `$GITHUB_STEP_SUMMARY` when that's
set. Exit codes: 0, or 1 with `-fail-over` when something is over, or 2
for an error (no file, a bad flag, a negative `-n`). Lines that aren't history entries are
skipped with a note, so one damaged line doesn't lose the rest.

**In all** is the product of (1 + change) over the runs that had a change
for the result, minus one. Each run's change compares base and head
measured together on one machine; medians from different runs may come
from different machines, so they are kept but not compared. A run with no
change for a result (new, removed, or base 0) doesn't count for it.

The **interval** after it comes from each run's own interval (`ci_lo`,
`ci_hi`, at the run's confidence). Each side's distance from the run's
change, in log terms, is read as a spread of the normal kind (half-width
over z), the spreads of all runs add as variances, and the result is
turned back into a 95% interval around the chained change. One run gives
back its own interval; many noisy runs give a wide one. Bytes, allocs and
size have no run intervals and add no spread. A time
result from a run without an interval (too few rounds) leaves the trend
without one, and then it never counts for `-fail-over`.

`-fail-over` counts a result only when some run gated it (pass, improved,
regression or inconclusive, so not info) and the low end of its interval
is over the limit. Noise that wanders upward over many runs doesn't trip
it; a creep that every run measured tightly does.
**Run by run** draws each run's change as a block: `·` for no change
(under 0.05%), `▁` for faster or smaller, and taller blocks for slower or
bigger, scaled to the largest change shown.

### jvq flaky

```
jvq flaky [flags] [RESULTS...]
```

The flaky test gate. Reads one CI run's test results, judges them against
a test history, adds the run to the history, and exits 1 only when a test
failed outside quarantine.

RESULTS are files, or folders whose `*.xml` and `*.json` files are read
(not their subfolders; a file in a folder that is neither format, such as
jvq's own JSON report, is left out with a note). Each file's format is told from its content: JUnit
XML when it starts with `<` (after a UTF-8 byte order mark, if any),
`go test -json` when it has a line that is a JSON test event. Lines that
aren't JSON in a `go test -json` file (stderr mixed in) are skipped. All
the files together are one run, so shards and a mix of formats add up.
UTF-16 files, other XML encodings than UTF-8, ASCII and Latin-1, a file
cut short, a run with no test results at all, and a run in which no test
ran (only Go packages that passed with no tests, as when a `-run` filter
matches nothing) are errors. Give one CI run's results: results of the
same tests from different platforms or matrix jobs in one call would make
a test that fails on one and passes on another look flaky.

| Flag | Default | Meaning |
|---|---|---|
| `-history FILE` | `[flaky] history` | the test history; `-` for none |
| `-commit SHA` | see below | the commit the results are for |
| `-no-write` | off | judge against the history without adding this run, as on a pull request |
| `-config FILE` | as for run | config file |
| `-json FILE` | none | also write the report as JSON |
| `-markdown FILE` | none | also write the report as Markdown |
| `-q` | off | print the verdict and notes, not the table |

Flags may come before or after RESULTS; everything after `--` is a
result. With no RESULTS it prints the history's quarantine and exits 0.

**Tests and attempts.** A test is named `classname.name` in JUnit (the
enclosing `<testsuite>`'s name when there's no classname) and
`package.Test/sub` in `go test -json`, so gotestsum's JUnit and its JSON
name a Go test the same way. Every result of a test in the run is one
attempt, in file order: `go test -count=N`, gotestsum `--rerun-fails`
(its JSON file holds each rerun), a testcase listed again, and Surefire's
rerun elements (`flakyFailure` and `flakyError` are failed attempts before
the pass, `rerunFailure` and `rerunError` failed attempts after the first
failure). A JUnit testcase with `<failure>` or `<error>` failed, with
`<skipped>` was skipped, and otherwise passed. In `go test -json`, a test
still running when its package failed is blamed only when it caused the
stop: under a `-timeout`, the tests Go names as running; otherwise, when
no test of the package has a fail event of its own (a panic gets one), the
tests still running. A test running beside one that panicked was
interrupted and gets no attempt (0.8.0); a
package that failed with no failing test (a build error, a crash before
any test) is the entry `package (package)`, which fails like a test, and a
package that passed adds a passing `package (package)` attempt, so that
entry can recover. A package that failed because of a test adds a skipped
`package (package)` attempt: no evidence either way, but it says which
tests are the package's (0.8.0). A package whose test binary didn't build, or whose
setup failed (Go 1.24's `FailedBuild`, or the `[build failed]` line of
older Go), is the entry `package (build)` instead: it only ever fails, so
it can never be flaky or quarantined, and a compile error blocks even when
`package (package)` is in quarantine for flaky leak checks. Those entries
aren't counted as tests in the summary.

In one run a test **passed** (every attempt that ran passed), **failed**
(no attempt passed), was **flaky** (failed and passed) or was skipped.

**The commit** is `-commit`, else `git rev-parse HEAD` in the current
folder, else `$GITHUB_SHA`, else `$CI_COMMIT_SHA`. The checkout comes
first because it's the code the tests ran on: on `pull_request_target`
and `workflow_run`, `GITHUB_SHA` is the base branch's latest commit, the
same for every pull request. With none, each run is its own code, and
only its own retries count as the same commit.

**The rules**, applied by replaying the whole history, oldest run first,
with this run last:

1. A **flaky commit** is a commit on which a test both passed and failed,
   in one run's retries or across runs of that commit. Runs on different
   commits never add up to one.
2. A test goes **into quarantine** when it has `quarantine_after` (2)
   flaky commits among its last `window` (100) runs (runs where it was
   only skipped don't count), unless its last
   `broken_after` attempts all failed.
3. It **leaves, stable again**, after `release_after` (30) passing runs in
   a row. A failed or flaky run starts the count again; skipped runs don't
   count either way. Its flaky commits are forgotten, so it needs
   `quarantine_after` new ones to come back.
4. It **leaves, broken**, when its last `broken_after` (3) attempts all
   failed, counting this run's retries and earlier runs. Its flaky commits
   are forgotten too.

**Pinned tests.** `[flaky]` `always` and `never` in jvq.toml take test
names (`*` matches anything, `?` one character), for tests the team has
already decided about:

- **always**: the test is in quarantine from its first run, with no
  evidence needed, and passing runs never take it out, so its failures
  don't block. It still breaks: when its last `broken_after` attempts all
  failed it leaves quarantine and blocks, and it goes back in once an
  attempt passes. Use it for a test known to be flaky that you'd rather
  not wait two flaky commits for.
- **never**: the test is never quarantined, however flaky; every failure
  blocks (a pass on retry still doesn't). Its flaky commits are still
  counted and shown. Use it for tests that guard something you won't let
  through on a coin flip, such as payments.

Pins apply to tests only: a Go package's own `(package)` and `(build)`
entries are never pinned, so a pattern such as `example.com/shop*` can't
quarantine a compile error or a crash in `TestMain`. A test matching both
lists is never quarantined. The same name in both is
an error. The pins apply to the whole history, since it's replayed each
time: pinning or unpinning a test changes its state at once. A pattern
that matches no test in the run or the history gets a note, since it's
most likely a typo. Rows of pinned tests carry `"pinned": "always"` or
`"never"`, and the table says so. Test names are as the table prints
them: `example.com/pkg.TestName` for Go, the JUnit class and name joined
with a dot.

**The gate:** a test that failed in this run blocks (exit 1) unless it is
in quarantine after this run. A broken test is never in quarantine, so it
blocks. A test that was flaky in this run doesn't block: it passed on
retry, though its failure counts towards quarantine. A quarantined test
that didn't run is listed as not in this run; one missing for a whole
window is no longer listed (most likely renamed or deleted).

**Not run.** When a Go package failed in this run, the tests that ran in
that package's last passing run and are missing now are listed as **not
run**: a crash (a panic, a `-timeout`) or `-failfast` stopped the package
before them. The last passing run (one whose `(package)` attempts all passed), so a
crash two runs in a row still shows them, and so does a crash that a
rerun (gotestsum `--rerun-fails`) passed: the package's `sp` counts as
failed. A package that failed before any test ran (a crash in `TestMain`)
is checked too. Subtests of a test that failed in this run and blocks
aren't listed (its failure stopped them, and says so); subtests a crash
stopped inside a quarantined test are listed, since that failure doesn't
block. A test renamed or deleted in a pull
request that also fails in that package, or left out by `-run`, is listed
too: the list says what wasn't checked, whatever the reason. A
`t.Parallel` test still paused when its package stopped never ran, so it
gets no attempt (it isn't a failure) and is listed as not run. A test belongs to the longest package name it starts with, followed
by a dot, among the packages the history knows (from their `(package)` or
`(build)` entries). Not-run tests don't block, since the crash itself
blocks unless it's in quarantine; the summary counts them and, in GitHub
Actions, one warning names them. A package that failed to build is left
out, and so are packages that passed, whose tests may come and go.

Without retries, a quarantined test that broke for real is listed as a
quarantined failure until its `broken_after`-th failure in a row, which
blocks. With `broken_after - 1` retries for a failed test (2 by default),
it blocks in the run that broke it; the price is that a flaky test that
fails every retry in one run is taken for broken and blocks. A run with
`-no-write` (a pull request) counts alone each time, so there only
retries can show a quarantined test as broken.

The table lists every test that failed, was flaky, is in quarantine, or
entered or left it in this run: its attempts in this run (`fp` failed,
then passed), its status, its flaky commits in the window, and its last 30
runs (`p` pass, `f` fail, `x` flaky, `s` skipped). Markdown goes to
`-markdown` and `$GITHUB_STEP_SUMMARY`; in GitHub Actions each blocking
failure is an error annotation and each quarantined failure a warning.
A test name with a character that isn't printable (a line break, a tab)
or starting with `:` is printed quoted, so it can't break the table or
start a GitHub workflow command.
Exit codes: 0 pass, 1 a test failed outside quarantine, 2 an error. A
history that can't be written is a warning, and the verdict stands.

The JSON report (`-json`):

```json
{"jvq": "0.9.0", "mode": "flaky", "commit": "4e1f0c2a…",
 "history": "jvq-tests.jsonl", "history_runs": 57, "written": true,
 "files": [{"path": "go-test.json", "format": "go-json", "tests": 412}],
 "settings": {"window": 100, "quarantine_after": 2, "release_after": 30, "broken_after": 3,
              "never": ["example.com/app.TestPay*"]},
 "result": {"verdict": "fail", "tests": 412, "failed": 1, "broken": 0,
            "quarantined_failures": 1, "flaky": 1, "not_run": 0, "in_quarantine": 1,
            "tests_of_note": [{"test": "web.Checkout.pays by card", "attempts": "f",
              "status": "quarantined", "blocks": false, "quarantined": true,
              "quarantined_since": "2026-10-28T09:12:00Z", "flaky_commits": 3,
              "pass_streak": 0, "fail_streak": 1, "runs": 57, "recent": "ppxppfpppf"}]}}
```

`status` is `failed`, `broken`, `quarantined` (failed in quarantine),
`flaky`, `passed`, `skipped`, `absent` or `not_run`; `event` is `entered`,
`released` or `broken` when this run moved the test in or out;
`pinned` is `always` or `never` for a pinned test. `settings` lists
`always` and `never` when they're set.

### jvq check

```
jvq check [-config FILE]
```

Reads the config the way `run` would and prints the settings it ends up
with. Exit 0 if the file is valid (or there is none), 2 if not.

### jvq version

Prints `jvq X.Y.Z (go version, os/arch)`.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | pass, or inconclusive without `fail_on_inconclusive`, or base failed with `-base-may-fail` |
| 1 | regression, or inconclusive with `fail_on_inconclusive` |
| 2 | error: bad flags or config, git problems, a build or run that failed, output with no results, an interrupt |

`jvq flaky` exits 0 when no test failed outside quarantine, 1 when one
did, and 2 for an error (an unreadable or empty results file, no tests, a
benchmark history given as the test history).

## jvq.toml

All settings are optional. Unknown keys are errors, so a typo can't fall
back to a default quietly.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `command` | string | see above | the benchmark command |
| `build` | string | none | runs once per side before the rounds |
| `adapter` | string | `"go"` | how to read results: `go`, `json`, `time`, `instructions`, `criterion`, `google-benchmark`, `pytest-benchmark` or `vitest` |
| `output` | string | none | a file the command writes its results to, read instead of its standard output; not with `time` |
| `size` | list of patterns | none | files whose size is gated, measured once per side after the build |
| `history` | string | none | a history file each run adds a line to |
| `history_keep` | integer, 1 to 1000000 | 1000 | how many runs the history file keeps, the newest |
| `rounds` | integer, 3 to 1000 | 10 | rounds per side to start with |
| `max_rounds` | integer, 3 to 1000 | 30 | most rounds per side while undecided |
| `warmup` | boolean | true | one thrown-away run per side first |
| `confidence` | number, 0.5 to below 1 | 0.95 | the intervals' confidence level |
| `timeout` | duration string | `"10m"` | limit per command run |
| `fail_on_inconclusive` | boolean | false | inconclusive exits 1 |
| `submodules` | boolean | false | check out submodules in both worktrees (`-submodules`) |
| `[budget]` `time` | percentage | `"5%"` | how much slower is allowed |
| `[budget]` `bytes` | percentage, amount or `"off"` | `"off"` | B/op allowance |
| `[budget]` `allocs` | percentage, amount or `"off"` | `"off"` | allocs/op allowance |
| `[budget]` `size` | percentage, amount or `"off"` | `"1%"` | how much a file listed in `size` may grow; an amount is in bytes |
| `[budget]` `instructions` | percentage or `"off"` | `"1%"` | how many more instructions the instructions adapter, or a json result in instructions, may count |
| `[budget.bench."PATTERN"]` | the five budget keys | | overrides for matching benchmarks (or file paths, for size); later tables win |
| `[ignore]` `bench` | list of patterns | none | benchmarks left out of the report |
| `[flaky]` `history` | string | none | `jvq flaky`'s test history file |
| `[flaky]` `history_keep` | integer, 1 to 100000 | 200 | how many runs the test history keeps, the newest |
| `[flaky]` `window` | integer, 1 to 100000 | 100 | a test's latest runs searched for flaky commits |
| `[flaky]` `quarantine_after` | integer, 1 to 1000, at most `window` | 2 | flaky commits that put a test in quarantine |
| `[flaky]` `release_after` | integer, 1 to 100000 | 30 | passing runs in a row that take it out |
| `[flaky]` `broken_after` | integer, 1 to 1000 | 3 | failed attempts in a row that make it broken: out of quarantine, blocking |
| `[flaky]` `always` | list of test names | none | tests in quarantine from their first run until they break (see Pinned tests) |
| `[flaky]` `never` | list of test names | none | tests never quarantined; their failures always block |

Budgets: `"5%"` allows 5% more than base; an amount such as `"0"` or `"64"`
allows that much more in the metric's unit (bytes or allocations per op);
`"off"` reports the metric without gating it. Time budgets must be
percentages, since an absolute time means something different on every
machine. Numbers may be written without quotes (`allocs = 0`).

Patterns match the benchmark name (`BenchmarkParse/small`) or the package
and name (`example.com/parse.BenchmarkParse/small`). `*` matches any run of
characters, slashes included; `?` matches one.

The file format is the TOML subset jvq needs: `key = value` lines, `[tables]`
with bare or quoted names, strings in double or single quotes, numbers,
booleans and arrays (which may span lines), and `#` comments. Inline tables,
dates, dotted keys and multi-line strings are errors.

## The go adapter

Reads standard Go benchmark output. A result line is a name starting with
`Benchmark`, an iteration count, then value-unit pairs:

```
pkg: example.com/parse
BenchmarkParse/small-8   1234567   912.3 ns/op   128 B/op   3 allocs/op
```

The `-8` GOMAXPROCS suffix is dropped. `pkg:` lines set the package for the
lines after them, so equal names in different packages stay apart. `ns/op` is
the time metric, `B/op` bytes, `allocs/op` allocations (these two need
`-benchmem` or `b.ReportAllocs()`). Any other unit, such as `MB/s` or a
custom `b.ReportMetric`, is reported but not gated. Every other line is
ignored. With `-count=N` a run prints N lines per benchmark, and jvq takes
their median as that run's value (see Samples).

## The harness adapters

Each reads one harness's own output as it is, and gives one time per
benchmark per run, in nanoseconds. If a run gives several (Google
Benchmark's `--benchmark_repetitions`), jvq takes their median, as for
every adapter (see Samples). The harness's own comparisons with an earlier
run are ignored.

**criterion** reads what Rust's criterion prints on standard output during
`cargo bench`:

```
parse/small             time:   [1.8615 µs 1.8732 µs 1.8884 µs]
a long benchmark name that wraps
                        time:   [191.47 ns 193.63 ns 195.52 ns]
```

The middle value, criterion's estimate, is the run's time; a name too long
for its column is on the line before. Throughput (`thrpt:`) and change
lines are skipped, including the `change:` block, with its own `time:` line
in percent, that criterion prints for a benchmark with a throughput. Colour
codes (`--color always`) are removed. The bencher format, from `cargo bench
-- --output-format bencher` or Rust's own `#[bench]`, works too:
`test parse_small ... bench: 1,873 ns/iter (+/- 21)`. It rounds to whole
nanoseconds, so a fast benchmark moves in coarse steps and one under half a
nanosecond prints 0 and is left out; criterion's default output is the
better one for jvq. Units ps, ns, µs (us), ms and s.

**google-benchmark** reads the JSON that Google Benchmark writes with
`--benchmark_format=json` (or to a file with `--benchmark_out=FILE
--benchmark_out_format=json` and `output = "FILE"`). Each iteration entry is
a sample of its `real_time`, in its `time_unit`; aggregate entries (mean,
median, stddev, cv) are skipped, unless there are only aggregates
(`--benchmark_report_aggregates_only`), when each benchmark's median stands
for the run. The name is `run_name`, so repetitions of one benchmark share
it. An entry with `error_occurred` fails the run with its message; a
skipped one (`SkipWithMessage`) is left out. Counters aren't read, and the
bare `NaN` and `Infinity` Google Benchmark can write for them are accepted.
Versions old enough to mark aggregates only by a name suffix (`BM_x_mean`)
aren't supported.

**pytest-benchmark** reads the file pytest-benchmark writes with
`--benchmark-json=FILE`; set `output` to it. Each benchmark's `stats.median`
is its time. Its name is the test's `name` (`test_parse[small]` for a
parametrised test), and the rest of its `fullname`, the file and any class,
is its group, so tests of the same name in two files stay apart. Two
benchmarks with the same group and name in one run are an error, since
jvq couldn't tell them apart.

**vitest** reads the file `vitest bench --run --outputJson FILE` writes, in
vitest 1 to 4 (vitest 5 dropped `--outputJson`); set `output` to it. Each
benchmark's `median` is its time, or its `mean` in vitest 1, which has no
median. The group is vitest's name for the file and `describe` blocks
around it, such as `parse.bench.js > json`; two benchmarks with the same
group and name in one run are an error.

For the JSON harnesses, the document may follow other output, as long as
it starts on a line of its own.

## File sizes

`size = ["target/release/app", "dist/*.js"]` lists files whose size jvq
gates. After the build (or before the first round, with no build), jvq
measures each regular file the patterns match on each side, in the folder
the command runs in. Patterns use `/` on every system, `*` matches within
one folder name (unlike benchmark patterns, where it crosses `/`), and a
pattern can't start with `/` or climb out with `..`; `jvq check` reports a
bad one. A folder isn't measured: name its files. A file matched by two
patterns counts once. `[ignore] bench` patterns hide size rows too. Each
file is a row with metric `size` and its path as the name, judged like
bytes: a regression when head's size is over base's plus the budget, 1% by
default, which for a small file is only a few bytes; an amount such as
`"2000"` suits those. A file on one side only is new or removed, not
gated; a pattern that matches nothing on either side is an error. With no
`build`, sizes are measured before the first run.

## The instructions adapter

On Linux, jvq runs each measured run of the command as

```
valgrind --tool=cachegrind --cache-sim=no --cachegrind-out-file=/dev/null \
  --trace-children=yes --log-file=<temp>/vg.%p sh -c '<command>'
```

and adds up the instruction counts (`I refs`) of every process it ran,
recording one sample per run named `command`, metric `instructions`,
judged like time: the Hodges-Lehmann shift in the log of the counts and
its interval against a percentage budget, `[budget] instructions`, 1% by
default. Whatever the command prints is ignored, and `output` can't be
used with it. jvq checks for valgrind and Linux before the first run and
says what's missing.

The command must do a fixed amount of work: a count tells how much work
was done, so a benchmark that runs for a set time does more work when the
machine is fast. For Go, build the test binary and run it with a fixed
count, `./x.test -test.run=NONE -test.bench=. -test.benchtime=1000x`;
`go test` as the command would count the toolchain's own work, and without
`-test.run=NONE` the tests too. Under valgrind the command runs 20 to 50
times slower, and there is no warm-up run. Counts vary a little from run to
run in programs whose work depends on timing (garbage collectors,
schedulers, Python's start-up), less than time does by far.

Unless GOMAXPROCS is set in jvq's environment, the command runs with
`GOMAXPROCS=1`. Valgrind runs one thread at a time, and Go's idle threads
spin while they wait; with more than one P that spinning is counted. A
test program with 8 allocating goroutines counted 0.66 G to 23.7 G
instructions at GOMAXPROCS=2 on a 2-CPU machine, and 215.0 M to 215.1 M at
1. Other runtimes with spinning worker threads may need the same.

Valgrind's counts are per process. A child that execs starts its count
afresh, but one that forks without exec (a subshell, a pipeline stage
running a shell builtin, Python's multiprocessing with fork) starts with
its parent's count so far, so that part is counted twice; run the program
itself, or `exec` it from a script. A process still running in the
background when the command exits isn't counted. When a run fails, or
valgrind wrote no count, the end of valgrind's own log is shown with the
error.

The adapter reports one result named `command`, so a
`[budget.bench."PATTERN"] instructions` override applies only when the
pattern matches `command`. JSON results with the unit `instructions` (from
perf or a harness that reads hardware counters, say) are the same metric:
judged like time, against `[budget] instructions`, and a count of zero or
less is an error.

## The time adapter

jvq times each run of the command itself, from the moment it starts the
shell to the moment the shell exits, and records one sample per run named
`command`, metric `time`. Whatever the command prints is ignored. The
measured time includes starting the shell and the program, so it suits
commands that run for a tenth of a second or more; for shorter work, have
the program time a loop itself and use the json adapter. There are no
bytes or allocation results.

## The json adapter

The command prints results as JSON, either one result per line:

```
{"name": "parse/small", "unit": "ns", "value": 912.3}
{"name": "parse/small", "unit": "ns", "value": 905.1}
{"name": "parse/small", "unit": "B", "value": 128}
```

or one document, `{"results": [result, ...]}`, or a bare array of results.
In the one-per-line form, lines that don't start with `{` (after spaces)
are skipped, so the command may print logs too, and so are JSON objects
with none of a result's fields, such as structured log lines. A line that
does look like a result must be a valid one. A document printed after some
other lines, even pretty-printed over many lines, is found too. A UTF-8
byte order mark is ignored; UTF-16 output is an error that says so.

A result has:

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | the benchmark's name, matched by `[budget.bench]` and `[ignore]` patterns |
| `unit` | yes | see below |
| `value` | one of these | one sample, a number |
| `values` | one of these | several samples, an array of numbers |
| `group` | no | keeps equal names apart, like a Go package; patterns may match `group.name` |

Any other field is an error, so a typo can't be lost quietly.

| Unit | Metric | Stored as |
|---|---|---|
| `ns`, `us` (or `µs`), `ms`, `s`, or spelled out (`seconds`, `milliseconds`) | time | nanoseconds |
| `B`, `bytes` | bytes | bytes |
| `allocs`, `allocations` | allocations | allocations |
| anything else | custom, reported but not gated | as given |

`/op` may follow any of these units (`ns/op`, `B/op`). Values must be
finite numbers (JSON has no NaN or Infinity, and JavaScript writes them as
`null`, which is an error here); time, bytes and allocations can't be
negative, and a time can't be 0. If no result in a run is gated, because
every unit is a custom one, the report says so.

## Samples

A run may print several values for one benchmark and metric: Go's
`-count`, several JSON lines, or `values`. In `jvq run`, jvq takes their
median as the run's one value. Values from one run share that run's state
of the machine, so they aren't independent observations, and counting each
as one would make the intervals too narrow; each run is one observation.
More values per run still help, by making each run's median steadier.
`jvq compare` can't tell runs apart in a saved file, so it uses every value
as it is; give it values from separate runs where you can.

## Verdicts

Per benchmark and metric:

| Verdict | Meaning |
|---|---|
| `regression` | over budget (see below); fails the run |
| `pass` | within budget |
| `improved` | a pass where head is clearly better: for time, the whole interval is below minus the budget; for bytes and allocations, the head median is lower |
| `inconclusive` | can't tell: the interval straddles the budget, too few samples, or a time of zero |
| `new` | only on head; never fails |
| `removed` | only on base; never fails |
| `info` | not gated: a custom metric, or a budget set to `"off"` |

**Time.** With base samples x and head samples y (each round gives one per
benchmark), jvq computes d = ln y − ln x for every pair, takes the median of
all the d (the Hodges-Lehmann estimate) and reports e^median − 1 as the
change. The confidence interval holds the d values at ranks k and N−k+1
(N = number of pairs), where k comes from the exact distribution of the
Mann-Whitney U statistic for n·m ≤ 2500 and from its normal approximation
above that (the Moses interval). It assumes only that head's distribution
is base's shifted, not that either is normal. With budget b:
regression if the interval's low end is above +b; pass if its high end is at
or below +b; inconclusive otherwise. At least 3 samples a side are needed,
and at 95% confidence about 4 a side for an interval to exist (4 against 4, or 3 against 5).

**Adding rounds.** Each extra pair of rounds is judged again at the same
confidence level. Looking again and again lets a borderline result cross
the line by chance more often than a single look would: in the recorded
accuracy runs, a change of exactly the budget (+5%) was called a regression
in 1 to 4 runs of 50 on a noisy cloud VM and in 5 or 6 of 50 on a quiet GitHub-hosted
runner. Changes well inside the budget, and no change at all, were never
flagged on either (0 of 50 at +2%, 0 of 200 with no change). Set
`max_rounds` equal to `rounds` for a single look.

**Bytes and allocations.** Compared on medians, with no statistics: a
regression when the head median is above the base median plus the budget
(or times one plus a percentage budget).

**Overall.** Regression if any gated row is a regression; else inconclusive
if any is inconclusive, or if no row is gated at all; else pass. Gated rows
are pass, improved, regression and inconclusive.

## JSON report

`-json FILE` writes:

```json
{
  "jvq": "0.2.0",
  "mode": "run",
  "verdict": "regression",
  "base": {"ref": "merge-base(origin/main, head)", "sha": "…"},
  "head": {"ref": "HEAD", "sha": "…"},
  "note": "",
  "command": "go test -run=NONE -bench=. -benchmem ./...",
  "build": "",
  "adapter": "go",
  "rounds": 14,
  "max_rounds": 30,
  "warmup": true,
  "confidence": 0.95,
  "order": ["base", "head", "head", "base", "…"],
  "seconds": 41.2,
  "counts": {"regression": 1, "pass": 6, "info": 2},
  "results": [
    {
      "package": "example.com/parse",
      "name": "BenchmarkParse/small",
      "metric": "time",
      "unit": "ns/op",
      "base": {"n": 14, "median": 912.3},
      "head": {"n": 14, "median": 1003.8},
      "change": 0.1002,
      "ci_lo": 0.081,
      "ci_hi": 0.119,
      "p": 0.0001,
      "budget": "5%",
      "verdict": "regression"
    }
  ]
}
```

`mode` is `run`, or `compare` for `jvq compare` (which leaves out the
commit hashes, and has 0 rounds and an empty order). `rounds` is how many rounds per side ran, extra ones included. `order` lists
the measured runs in the order they ran. `change`, `ci_lo` and `ci_hi` are
relative (0.1 means +10%); `ci_lo`, `ci_hi` and `p` (the two-sided
Mann-Whitney p-value for no difference) appear for time only. `change` is
left out when base was zero. `note` explains a non-obvious verdict or base.
Medians are in the metric's unit (nanoseconds for time).

## History file

`jvq run -history FILE` (or `history` in jvq.toml) adds one line to FILE
after the report, whatever the verdict, creating the file if needed and
keeping the newest `history_keep` lines. It writes a temporary file beside
it and renames it into place, so a crash can't leave it half written. Two
runs writing at once can lose one of their lines. A line is a JSON object:

```json
{"jvq": "0.5.1", "date": "2026-10-19T08:00:00Z",
 "base": {"ref": "head^", "sha": "…"}, "head": {"ref": "HEAD", "sha": "…"},
 "verdict": "pass", "confidence": 0.95,
 "results": [{"package": "example.com/parse", "name": "BenchmarkParse",
              "metric": "time", "unit": "ns/op", "base_median": 912.3,
              "head_median": 920.1, "change": 0.0085,
              "ci_lo": -0.004, "ci_hi": 0.021, "verdict": "pass"}]}
```

(Shown over several lines here; in the file it's one.) `change` is left
out when there is none, such as for a new benchmark; `ci_lo` and `ci_hi`
appear when the run had an interval.

A relative FILE is relative to the folder jvq runs in. When the run's base
would be head's parent (a push to the default branch) and the history's
last line ended at an ancestor of head, the run starts from that commit
instead (base ref `history`), so a push of several commits measures them
all; if head is the last line's head (a re-run), it reuses that line's
base. A line with the same base and head as the new one is replaced, so a
re-run of the same job doesn't count twice. `-history -` turns off the
history jvq.toml sets.

In GitHub Actions, the action's `history` input keeps the file in the
Actions cache under the key `history-key` (by default one per workflow,
job and runner OS). It restores the newest saved copy before the run.
Runs that write (`history-write`: `auto` is push, workflow_run and
workflow_dispatch runs on the default branch; `true` is every run; `false`
none) add a line and save it under a new key; the others run with
`-history -`. Every run that wasn't cancelled then shows `jvq history`,
and `history-fail-over` applies on writing runs only, so a creep on the
default branch fails the push that measured it rather than every pull
request after it. Use the `history` input rather than the jvq.toml key,
so the cache goes with it. GitHub deletes a cache that hasn't been read in
7 days, so a history needs a run at least weekly; two runs at once can
both restore the same copy, and the later save then drops the earlier
line, which a `concurrency` group on the workflow avoids.

In GitLab, `JVQ_HISTORY: "true"` keeps `jvq-history.jsonl` in a cache
keyed by the job name, which default-branch pipelines save and merge
request pipelines only pull, and writes `jvq-history.md` as an artifact.
`JVQ_HISTORY_FAIL_OVER` applies on the runs that add to it. GitLab keeps
protected branches' caches apart from others by default, so merge request
pipelines may not see the default branch's history unless the project
shares caches between protected and unprotected branches.

## Test history file

`jvq flaky` adds one line per run to its history (`-history` or
`[flaky] history`), after judging, creating the file if needed and keeping
the newest `history_keep` lines. Like the run history, it's rewritten
through a temporary file beside it, a symlink is followed, and the file
keeps its mode. Since 0.9 the file is compact: each run's line gives its
tests as numbers in one list of names, written as the file's first line
and again as its last (so a file cut short at the end, or trimmed by an
older jvq, still has one):

```json
{"kind": "tests", "jvq": "0.9.0", "date": "2026-11-04T08:00:00Z",
 "commit": "4e1f0c2a…", "names_id": "9c1d2e3f4a5b6c7d",
 "results": {"p": "0-41,43-1999", "fp": "42", "f": "2000"}}
{"kind": "test-names", "id": "9c1d2e3f4a5b6c7d",
 "names": ["example.com/app.TestParse", "example.com/app.TestUpload", …]}
```

`results` maps each test's attempts to the numbers of the tests that had
them, in the sorted `names` list, as rising numbers and ranges (`0-41`).
Attempts are letters in order: `p` pass, `f` fail, `s` skip. A test with
no letters is no evidence either way, like a skip. `id` is a hash of the
names, so a line can only be read with the list it was written with.
Nothing else is stored; quarantine is replayed from these lines every
time. A re-run of the same commit adds a line of its own, since it's more
evidence. A line that isn't a valid entry (cut short, binary junk, letters
other than p, f and s, numbers past the list or out of order, a test
twice, a names list that doesn't match its id, a `kind` other than
`tests`) is skipped with a note, and left in the file until it's trimmed.
A compact line whose names list is gone from the file can never be read
again, so the next write drops it. A history is read into at most 5
million test results, newest runs first, so a small crafted file can't
fill the memory. Older runs past that are left out with a note (not as
damage), and dropped on the next write. 2000 tests over 200 runs are
400,000 results; with 25,000 tests or more, lower `history_keep` (not
below `window`) so every run kept is read.
A missing file is an empty history. A file holding `jvq run`'s benchmark
history is refused, not written. Two runs writing at once can lose one of
their lines.

jvq 0.7 and 0.8 wrote each test's name on every line:

```json
{"kind": "tests", "jvq": "0.7.0", "date": "2026-10-31T08:00:00Z",
 "commit": "4e1f0c2a…", "tests": {"example.com/app.TestParse": "p",
 "example.com/app.TestUpload": "fp", "web.Checkout.pays by card": "f"}}
```

Those lines are still read, and the next run that writes converts the
file. For 2000 Go tests named like
`github.com/acme/shop/internal/pkg07.TestSomething0123/case_4` over 200
runs, the old format came to 26.8 MB and the compact one to 319 KB
(`TestCompactHistorySize`). jvq 0.8 and earlier can't read compact lines:
they see an empty history, so nothing is in quarantine and every failure
blocks, which is the safe side. They keep the lines they can't read, and
since a copy of the names line is last, it's trimmed only after every
line that needs it, so going back to 0.9 later loses nothing.

## JUnit report

`-junit FILE` (on `run` and `compare`) writes one test suite named `jvq`
with a test case per gated result, named after the benchmark and metric,
with class `jvq` or `jvq.` plus the package or group. A regression is a
failure, with the change and budget as its message; inconclusive is
skipped, with the reason; pass and improved pass. Results that aren't
gated (new, removed, info) aren't listed. Each case's `system-out` has
the medians, change, interval and budget.

## GitLab CI

[ci/gitlab/jvq.yml](../ci/gitlab/jvq.yml) defines a hidden job `.jvq` to
include and extend. Its variables:

| Variable | Default | Meaning |
|---|---|---|
| `JVQ_VERSION` | the release the file came with | a release tag, or `latest` |
| `JVQ_COMMAND` | empty | the benchmark command; empty uses jvq.toml or the Go default |
| `JVQ_ARGS` | empty | more flags for `jvq run`, split on spaces |
| `JVQ_DIR` | `.` | where to run, relative to the repository |
| `GIT_DEPTH` | `"0"` | a full clone, so the base commit is there |
| `JVQ_HISTORY` | `"false"` | `"true"` keeps a history in the job's cache; default-branch pipelines add to it |
| `JVQ_HISTORY_FAIL_OVER` | empty | with history, fail a default-branch run when a gated result has moved more than this in all, beyond its noise |
| `JVQ_CACHE_POLICY` | `pull-push` | set to `pull` by the merge request rule, so merge requests don't save the history |

It runs on `merge_request_event` pipelines and on the default branch,
fetches the merge request's target branch, downloads
`jvq_linux_<arch>.tar.gz` for x86-64 or ARM64, checks it against the
release's SHA256SUMS, and writes jvq.xml (a `junit` report), jvq.md and
jvq.json as artifacts, kept even when the job fails. When jvq stops with
an error before its report (exit 2), those files don't exist and GitLab
warns that the artifacts are missing, beside the real error. It fetches
the merge request's target branch only when GitLab names no base commit,
and carries on if that fetch fails. The image needs `curl`, `git`, `tar`
with gzip, `sha256sum`, `grep`, `mktemp` and `uname`.

## Worktrees and cleanup

The temp folder is `jvq-run-*` in the system temp directory, with a
`jvq-owner` file holding the process id. On every run, jvq removes any such
folder whose process is gone and runs `git worktree prune`. So an interrupt
cleans up at once, and a run killed outright (SIGKILL, a cancelled CI job)
is cleaned up by the next run on that machine.

**Submodules.** With `submodules = true`, each submodule is fetched from
its URL into each worktree (git keeps a worktree's submodules apart from
the main checkout's), with the credentials git has for that URL. jvq turns
off git's password prompt for this, and runs ssh in batch mode unless
`GIT_SSH_COMMAND`, `GIT_SSH` or `core.sshCommand` is set, so a submodule it can't reach is an
error that names it rather than a run that waits for an answer. In GitHub Actions,
`actions/checkout` with `submodules: true` sets up access for submodules
in the same organization; private ones elsewhere need a token, as they
would for the checkout. Fetching takes time on every run, once per side.

## The GitHub Action

`uses: just-very-quickly/jvq@vX.Y.Z` runs on Linux, macOS and Windows
runners (x86-64 and ARM64). It downloads `jvq_<os>_<arch>` from the release
named by the tag (or `version`, or the latest release when referenced by a
branch), checks it against the release's SHA256SUMS, puts it on the PATH and
runs `jvq run` with the inputs:

| Input | Becomes |
|---|---|
| `command` | `-- "<command>"` |
| `base` | `-base` |
| `config` | `-config` |
| `args` | more flags, split on spaces |
| `working-directory` | where jvq runs |
| `version` | which release to download |
| `history` | a history file, relative to `working-directory`, kept in the Actions cache (see History file) |
| `history-write` | `auto`, `true` or `false`: which runs add to the history |
| `history-fail-over` | with `history`, `jvq history -fail-over`, on runs that write |
| `history-key` | the cache key prefix; empty is one per workflow, job and runner OS |
| `jvq-path` | a folder with a jvq binary to use instead of downloading one (for testing the action) |

Check out with `fetch-depth: 0` so the base commit is there.

With `flaky` set (0.8.0), the action runs `jvq flaky` instead of `jvq run`:

| Input | Becomes |
|---|---|
| `flaky` | the results: files or folders, separated by spaces or new lines, after `--` |
| `flaky-history` | `-history`, relative to `working-directory` (default `jvq-tests.jsonl`), kept in the Actions cache |
| `flaky-write` | `auto` (default): push and `workflow_dispatch` runs on the default branch, and `schedule` runs (always on the default branch), add to the history; others get `-no-write`; `true` every run; `false` none |
| `flaky-key` | the cache key prefix; empty is `jvq-tests-<workflow>-<job>-<matrix job index>-<runner OS>`, so each matrix leg keeps its own history. The index is the leg's position: adding or reordering matrix entries hands legs each other's histories, so set `flaky-key` from the matrix values when the matrix changes often |
| `flaky-args` | more flags, split on spaces, such as `-json flaky.json` |
| `config` | `-config` |

The cache is restored from the newest entry with the prefix (a pull
request sees its base branch's), and saved under a new key after a run
that writes, unless the run was cancelled or left no file. The step fails
with `jvq flaky`'s exit code, so a failure outside quarantine fails the
job; the tests' own step must not stop the job first (`|| true`, or
`continue-on-error`; for a pipeline such as `go test -json ./... | tee
go-test.json`, put `|| true` after the whole pipeline, since GitHub's bash
runs with `pipefail`). The history file must be inside the workspace, where
the cache can save it. The other inputs are for the benchmark gate and are
ignored.
