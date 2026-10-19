# jvq reference

Everything jvq v0.5 does, in one place. The [README](../README.md) is the
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
| `-adapter NAME` | `go` | how to read results: `go`, `json`, `time`, `criterion`, `google-benchmark`, `pytest-benchmark` or `vitest` |
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
runs measured it, its change in all, how many runs called it a
regression, and its change run by run for the latest 30.

| Flag | Default | Meaning |
|---|---|---|
| `-n N` | all | only the newest N runs |
| `-bench PATTERN` | all | only benchmarks matching the pattern |
| `-metric NAME` | all | only this metric |
| `-fail-over PCT` | none | exit 1 if any time, bytes, allocs or size result has moved more than this in all |
| `-markdown FILE` | none | also write the table as Markdown |

It also appends the Markdown table to `$GITHUB_STEP_SUMMARY` when that's
set. Exit codes: 0, or 1 with `-fail-over` when something is over, or 2
for an error (no file, a bad flag). Lines that aren't history entries are
skipped with a note, so one damaged line doesn't lose the rest.

**In all** is the product of (1 + change) over the runs that had a change
for the result, minus one. Each run's change compares base and head
measured together on one machine; medians from different runs may come
from different machines, so they are kept but not compared. A run with no
change for a result (new, removed, or base 0) doesn't count for it.
**Run by run** draws each run's change as a block: `·` for no change
(under 0.05%), `▁` for faster or smaller, and taller blocks for slower or
bigger, scaled to the largest change shown.

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

## jvq.toml

All settings are optional. Unknown keys are errors, so a typo can't fall
back to a default quietly.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `command` | string | see above | the benchmark command |
| `build` | string | none | runs once per side before the rounds |
| `adapter` | string | `"go"` | how to read results: `go`, `json`, `time`, `criterion`, `google-benchmark`, `pytest-benchmark` or `vitest` |
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
| `[budget]` `time` | percentage | `"5%"` | how much slower is allowed |
| `[budget]` `bytes` | percentage, amount or `"off"` | `"off"` | B/op allowance |
| `[budget]` `allocs` | percentage, amount or `"off"` | `"off"` | allocs/op allowance |
| `[budget]` `size` | percentage, amount or `"off"` | `"1%"` | how much a file listed in `size` may grow; an amount is in bytes |
| `[budget.bench."PATTERN"]` | the four budget keys | | overrides for matching benchmarks (or file paths, for size); later tables win |
| `[ignore]` `bench` | list of patterns | none | benchmarks left out of the report |

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
{"jvq": "0.5.0", "date": "2026-10-19T08:00:00Z",
 "base": {"ref": "head^", "sha": "…"}, "head": {"ref": "HEAD", "sha": "…"},
 "verdict": "pass",
 "results": [{"package": "example.com/parse", "name": "BenchmarkParse",
              "metric": "time", "unit": "ns/op", "base_median": 912.3,
              "head_median": 920.1, "change": 0.0085, "verdict": "pass"}]}
```

(Shown over several lines here; in the file it's one.) `change` is left
out when there is none, such as for a new benchmark.

In GitHub Actions, the action's `history` input keeps the file in the
Actions cache: it restores the newest saved copy before the run, adds a
line on runs that aren't pull requests on the default branch, saves it
under a new key, and runs `jvq history` (with `history-fail-over`, if set)
on every run. In GitLab, `JVQ_HISTORY: "true"` keeps `jvq-history.jsonl`
in the job's cache and writes `jvq-history.md` as an artifact. GitLab keeps
protected branches' caches apart from others by default, so merge request
pipelines may not see the default branch's history.

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
| `JVQ_HISTORY` | `"false"` | `"true"` keeps a history in the cache; default-branch pipelines add to it |
| `JVQ_HISTORY_FAIL_OVER` | empty | with history, fail when anything has moved more than this in all |

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
| `history` | a history file kept in the Actions cache (see History file) |
| `history-fail-over` | with `history`, `jvq history -fail-over` |

Check out with `fetch-depth: 0` so the base commit is there.
