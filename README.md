# jvq

A performance budget gate for CI. Put it in your pipeline and it fails a pull
request when the change makes your benchmarks meaningfully slower than the
base branch. If the change is within the noise, it passes.

It's one binary. No server, no account, no stored history. Every run measures
the base commit and the change on the same machine, in the same job, taking
turns, and then compares the two with rank statistics.

```sh
jvq run -- go test -run=NONE -bench=. -benchmem ./...
```

That checks out the base and head commits into two temporary git worktrees,
runs the benchmarks on each in alternating rounds, prints a verdict table,
adds a Markdown report to the CI job summary, and exits 0 for pass, 1 for
regression or 2 for an error.

It works for any language. jvq reads Go's benchmark output as it is, times
any command whole, or reads a few lines of JSON your benchmark prints:

```sh
jvq run -adapter time -- python3 bench.py      # time the whole command
jvq run -adapter json -- node bench.js         # read {"name", "unit", "value"} lines
```

**Status: v0.3.0.** Go benchmark output, Rust's criterion, Google
Benchmark, pytest-benchmark and vitest as they are, whole-command timing, a
small JSON format, `jvq compare` for two saved outputs, time, bytes,
allocation and file-size budgets, adaptive rounds, a GitHub Action, Linux,
macOS and Windows. jvq is open source under the Apache License 2.0.

## Why it exists

Speed regressions slip in one small PR at a time, because nobody runs
benchmarks by hand on every change. Teams that do run them in CI hit noise:
shared runners are often 5 to 20% off from one run to the next. Comparing
today's run against last week's numbers from a different machine then cries
wolf, people learn to ignore it, and the regressions get in anyway.

Timing two runners on different days, in different weather, tells you little.
Timing them on the same track at the same time, taking turns, tells you a
lot. jvq does the second thing. When the weather is too bad to tell, it says
so instead of guessing.

## How it decides

1. **Two commits.** Base is where your branch left the base branch (in a
   GitHub pull request, `GITHUB_BASE_REF`; otherwise origin's default branch,
   `main` or `master`). On the base branch itself, base is head's parent, so
   each push is checked against the commit before it. `-base` picks any
   commit.
2. **Two worktrees.** Both commits are checked out with `git worktree` into
   a temp folder, built once each if you give a `build` command, and removed
   afterwards. A run killed with SIGKILL leaves its worktrees behind; the
   next run removes them.
3. **Interleaved rounds.** One warm-up run per side, thrown away. Then rounds
   in the order base, head, head, base, base, head... (ABBA). If the machine
   slowly gets faster or slower during the run, both sides feel it equally.
4. **Rank statistics.** For time, jvq takes the Hodges-Lehmann estimate of
   the shift in log time (so a ratio, such as +7.2%) and its 95% confidence
   interval from the Mann-Whitney distribution. No assumption that timings
   are normally distributed. They aren't: they have long tails from
   interrupts and cache misses, and one wild value moves a rank statistic by
   one rank, not by its size.
5. **Three verdicts.** With a 5% budget:
   - **regression**: the whole interval is above +5%. Even the kindest
     reading of the data says head is over budget.
   - **pass**: the whole interval is at or below +5%.
   - **inconclusive**: the interval straddles +5%. Can't tell at this noise.
6. **Adaptive rounds.** While any result is inconclusive, jvq adds rounds,
   two at a time, up to `max_rounds` (30). Clear cases stop after 10. Each
   extra look is a new chance for a result right at the budget to tip
   over, which is part of why a change of exactly +5% was flagged in 1 to 6
   runs of 50.
7. **Exact gates.** Bytes and allocations per op hardly vary between runs, so
   they're compared on medians, without statistics. `allocs = "0"` fails a
   change that adds even one allocation per op.

Inconclusive passes by default (exit 0) with a note in the report. Set
`fail_on_inconclusive = true` if you'd rather block.

## How well it works

jvq's own test suite measures its false-alarm rate and its detection rate,
by running the real binary hundreds of times on a fixture benchmark whose
cost is set by a constant. [test/results/accuracy.md](test/results/accuracy.md)
has the full table. It was recorded on a shared two-core cloud VM where the
same benchmark binary, run 40 times in a row, spread by 7.2% from run to run
([test/results/noise.json](test/results/noise.json)), against a 5% budget.

| Head | Runs | Flagged as regression | Right answer |
|---|---:|---:|---|
| no change | 200 | 0 (0%) | no regression |
| 2% more work | 50 | 0 (0%) | no regression |
| 5% more work | 50 | 4 (8%) | no regression, at the budget's edge |
| 10% more work | 50 | 38 (76%) | regression |
| 20% more work | 50 | 50 (100%) | regression |
| 50% more work | 50 | 50 (100%) | regression |
| one more allocation per op | 100 | 100 (100%) | regression |
| no change, allocations gated | 100 | 0 (0%) | no regression |

The runs that weren't flagged passed or came back inconclusive. Clear cases
took the 10 starting rounds; close ones used more, up to 30 per side, and a
time run took 1.5 to 3.5 seconds on this fixture, on a day the VM's
timings spread 7.1% from run to run. How much a 10% slowdown gets caught
depends on that noise: earlier recordings on the same kind of VM caught
42 of 50 at 8.9%, 34 of 50 at 7.2% and 48 of 50 at 4.6%. None of them
raised a false alarm.

On a GitHub-hosted runner (ubuntu-latest, 4 cores) the same suite saw only
0.6% run-to-run noise, and every clear case was decided in the first 10
rounds: all 200 no-change runs and all 50 at +2% passed, and every run at
+10%, +20% and +50% was caught. A change of exactly the budget, +5%, was
flagged in 6 of 50 runs: with so little noise, adding rounds gives a change
sitting on the line more chances to tip over (see docs/reference.md).
[test/results/github/](test/results/github/accuracy.md) has that recording.

Near the budget, jvq would rather say "can't tell" than guess. That's the
design: a gate that cries wolf gets switched off.

### Harder settings

[test/results/scenarios.md](test/results/scenarios.md) records more
situations, each with a known answer, on the same two machines:

| Setting | Cloud VM (2 cores) | GitHub runner (4 cores) |
|---|---|---|
| A neighbour keeping half the CPUs busy and churning 512 MB: no change, 100 runs | 0 false alarms | 0 false alarms |
| The same neighbour, 10% more work, 50 runs | 38 caught, 12 inconclusive | 50 caught |
| Every run 2% slower than the one before, no change, base and head taking turns (ABBA), 50 runs | 0 false alarms | 0 false alarms |
| The same drift with all of base run before all of head, 50 runs | 44 false alarms | 50 false alarms |
| A Python script timed whole (time adapter): no change, 60 runs; 20% more work, 30 runs | 0 false alarms; 28 caught | 0 false alarms; 30 caught |
| The same script reporting its own timings (json adapter): no change, 60 runs; 20% more work, 30 runs | 0 false alarms; 29 caught | 0 false alarms; 30 caught |

The drift rows are why jvq takes turns. A machine that slows down during a
job makes whatever runs second look slower; run in sequence, that faked a
regression in most runs, while taking turns faked none. Taking turns can't
remove the extra spread such drift causes, though: most of those runs came
back inconclusive rather than pass. The Python rows at 10% more work, where
the VM caught 8 and 20 of 30 and the GitHub runner all 30, are in the file.

The real harnesses, end to end through `jvq run`, 10 runs each:

| Harness | Cloud VM: no change | VM: 20% more work | GitHub runner: no change | GitHub: 20% more work |
|---|---|---|---|---|
| pytest-benchmark | 1 false alarm, 2 pass, 7 inconclusive | 10 caught | 10 pass | 10 caught |
| Google Benchmark (Python bindings) | 0 false alarms | 8 caught, 2 inconclusive | 10 pass | 10 caught |
| vitest bench | 0 false alarms | 10 caught | 10 pass | 10 caught |
| criterion | 0 false alarms | 10 caught | 10 pass | 10 caught |

pytest-benchmark was the noisiest: with a short time budget per test
(`--benchmark-max-time=0.2`), its medians wandered enough on this VM that
one no-change run came out 26% slower, with its whole interval over the
budget. A longer `--benchmark-max-time` is the first thing to raise if it's
noisy for you; this recording didn't try one.

The VM's accuracy table is from jvq 0.4.0 and its scenarios from 0.3.0;
nothing those scenarios measure changed in 0.4. The GitHub runner's are
from the 0.3.0 release commit (0fa5121).

## Any language

The adapter says how jvq gets numbers out of a run.

- **`go`** (the default) reads `go test -bench` output.
- **`time`** times the whole command, from start to exit, and calls the
  result `command`. Nothing to change in your code; the start-up of the
  program is part of what's measured, so a slow start counts. Best for
  commands that take a tenth of a second or more.
- **`json`** reads results your benchmark prints, one per line:

  ```json
  {"name": "parse/small", "unit": "ns", "value": 912.3}
  {"name": "parse/small", "unit": "B", "value": 128}
  ```

  Units `ns`, `us`, `ms` and `s` are time; `B` is bytes; `allocs` is
  allocations; any other unit is reported but not gated. `"values": [...]`
  gives several samples at once, and a single `{"results": [...]}` document
  works too. Lines that don't start with `{` are skipped, so the benchmark
  can print other things. When a run prints several values for one
  benchmark, jvq takes their median as that run's value.
  [docs/reference.md](docs/reference.md#the-json-adapter) has the whole
  format.

Set `output = "results.json"` (or `-output`) when the benchmark writes its
results to a file instead of printing them. A Python benchmark could end
with:

```python
import json, time
for _ in range(5):
    t = time.perf_counter_ns(); parse(sample); ns = time.perf_counter_ns() - t
    print(json.dumps({"name": "parse", "unit": "ns", "value": ns}))
```

### Benchmark harnesses, read as they are

| Adapter | Harness | Command | Reads |
|---|---|---|---|
| `criterion` | Rust's [criterion](https://github.com/bheisler/criterion.rs) | `cargo bench` | the `time: [low estimate high]` lines it prints; also `test x ... bench: N ns/iter` lines |
| `google-benchmark` | [Google Benchmark](https://github.com/google/benchmark), C++ or its Python bindings | `./bench --benchmark_format=json` | each repetition's real time |
| `pytest-benchmark` | [pytest-benchmark](https://github.com/ionelmc/pytest-benchmark) | `pytest --benchmark-json=bench.json`, with `output = "bench.json"` | each test's median |
| `vitest` | [vitest bench](https://vitest.dev/guide/features.html#benchmarking), versions 1 to 4 | `vitest bench --run --outputJson bench.json`, with `output = "bench.json"` | each benchmark's median |

For example, a Rust crate:

```toml
adapter = "criterion"
build   = "cargo bench --no-run"
command = "cargo bench"
```

Each worktree is a fresh checkout, so cargo builds from scratch in each
jvq run. On Linux and macOS, `--target-dir "$HOME/.cache/jvq-$JVQ_SIDE"` in
both commands keeps one build folder per side between runs. Each adapter gives one time per benchmark
per run; jvq's own rounds do the rest.
The harness's own comparison with its last run (criterion's "change:"
lines, for one) is ignored, since jvq measures base and head itself.

## A file-size budget

```toml
size = ["target/release/app", "dist/*.js"]   # measured after the build

[budget]
size = "1%"     # the default once size is set; an amount such as "50000" works too
```

jvq measures each file the patterns match, on both sides, once the build
has run (or before the first round, with no build), and fails the run when
one grows by more than its budget. Patterns are relative to where the
command runs, `*` matches within one folder, and `[budget.bench."dist/*"]`
can set a different budget for some files. A file on one side only is
reported as new or removed, and not gated, so a renamed binary slips
through. A pattern that matches nothing on either side is an error, and so
is a folder: name the files, since jvq doesn't add sizes up. 1% of a small
file is only a few bytes, so give small files an amount such as `"2000"`.
With no `build`, sizes are measured before the first run, so a command
that builds its own output needs a `build` step for the size gate to see
it.

## Two saved outputs: jvq compare

```sh
go test -run=NONE -bench=. -count=10 > old.txt   # before
go test -run=NONE -bench=. -count=10 > new.txt   # after
jvq compare old.txt new.txt
```

`jvq compare` judges two saved outputs with the same statistics, budgets and
exit codes as `jvq run`. Files ending in `.json` are read with the json
adapter, others as Go output (or name it with `-adapter`). Each file needs
several samples per benchmark. The two runs weren't interleaved, so anything
that changed on the machine between them counts as a change; `jvq run` is
the safer gate, and `compare` suits a quick check on a quiet machine.

## In GitHub Actions

```yaml
name: benchmarks
on: [pull_request]
jobs:
  jvq:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0   # jvq needs the base commit
      - uses: actions/setup-go@v7
        with:
          go-version: stable
      - uses: just-very-quickly/jvq@v0.5.0
        with:
          command: go test -run=NONE -bench=. -benchmem ./...
```

The action downloads the release binary for the runner, checks its SHA256
against the release's SHA256SUMS, and runs `jvq run`. It needs no token and
no extra permissions; the report goes to the job summary. Inputs: `command`,
`base`, `config`, `args` (more flags), `working-directory` and `version`.

## A history across runs

Each run judges one change against its base, so a slowdown that arrives
in small steps, each inside the budget, passes every time. Keep a history
and it shows:

```yaml
      - uses: just-very-quickly/jvq@v0.5.0
        with:
          history: jvq-history.jsonl
          history-fail-over: 10%     # optional: fail when anything has moved more than this in all
```

With `history`, the action keeps the file in the Actions cache. Runs on
the default branch add a line to it, pull requests don't, and every run
prints the trend to the job summary:

```
jvq history: 24 runs, 2026-10-01 (4be1c0f2a9) to 2026-10-19 (9d01e77c3b) in jvq-history.jsonl

BENCHMARK       METRIC  RUNS  IN ALL  REGRESSIONS  RUN BY RUN
BenchmarkParse  time    24    +11.8%  0            ··▂·▃▁·▂▂·▃·▂·▁▂··▃▂·▂▂·
BenchmarkWrite  time    24    -2.1%   0            ·▁··▁·▂▁··▁···▁·▂··▁···▁
```

*In all* chains each run's own change, base to head. Medians from
different runs come from different machines and can't be compared, but
each run's change was measured on one machine, so the changes can be
multiplied together: ten runs at +1% each make about +10.5%.

Anywhere else, `jvq run -history FILE` adds a line to FILE (or set
`history = "FILE"` in jvq.toml), and `jvq history FILE` shows the trend,
with `-n 30` for the latest 30 runs, `-bench` and `-metric` to narrow it,
`-markdown` to write it as Markdown, and `-fail-over 10%` to exit 1 when
something has moved more than that. The file is JSON, one line per run,
and keeps the newest 1000 (`history_keep`).

## In GitLab CI

```yaml
include:
  - remote: https://raw.githubusercontent.com/just-very-quickly/jvq/v0.5.0/ci/gitlab/jvq.yml

benchmarks:
  extends: .jvq
  image: golang:1.26
  variables:
    JVQ_COMMAND: go test -run=NONE -bench=. -benchmem ./...
```

The `.jvq` job ([ci/gitlab/jvq.yml](ci/gitlab/jvq.yml)) runs in merge
request pipelines and on the default branch. It sets `GIT_DEPTH: "0"`,
fetches the target branch, downloads the release and checks it against
SHA256SUMS, and runs `jvq run`. A regression fails the job; the result
shows in the merge request's test summary through a JUnit report, and
jvq.md and jvq.json are kept as artifacts. Set `JVQ_ARGS` for more flags,
`JVQ_DIR` to run in a subfolder, and `JVQ_VERSION` to pin a release.
`JVQ_HISTORY: "true"` keeps a history in the GitLab cache, added to by
default-branch pipelines, with `JVQ_HISTORY_FAIL_OVER` as the limit. The
image needs `curl`, `git`, `tar`, `sha256sum` and whatever your benchmarks
need; Debian-based images such as `golang:1.26` have them, and on Alpine
add `before_script: apk add --no-cache curl git`. If your GitLab can't
reach raw.githubusercontent.com, copy the file into your repository and
`include: local:` it. The merge request rule gives you merge request
pipelines beside branch pipelines; GitLab's `workflow:rules` can keep one
of the two.

jvq picks the base from GitLab's own variables: in a merged results
pipeline or merge train, the target branch the change was merged into or
rebased onto; otherwise the merge request's diff base
(`CI_MERGE_REQUEST_DIFF_BASE_SHA`); on the default branch, the commit
before. This was tested with GitLab's variables set by hand and with
gitlab-ci-local, which ran the template end to end and downloaded and
checked a real release, but not yet on gitlab.com itself.

## Anywhere else

Ready-built binaries are on the
[releases page](https://github.com/just-very-quickly/jvq/releases/latest).
These links always fetch the latest release:

| System | Archive |
|---|---|
| Linux, x86-64 (static) | [jvq_linux_amd64.tar.gz](https://github.com/just-very-quickly/jvq/releases/latest/download/jvq_linux_amd64.tar.gz) |
| Linux, ARM64 (static) | [jvq_linux_arm64.tar.gz](https://github.com/just-very-quickly/jvq/releases/latest/download/jvq_linux_arm64.tar.gz) |
| macOS, Apple silicon | [jvq_darwin_arm64.tar.gz](https://github.com/just-very-quickly/jvq/releases/latest/download/jvq_darwin_arm64.tar.gz) |
| macOS, Intel | [jvq_darwin_amd64.tar.gz](https://github.com/just-very-quickly/jvq/releases/latest/download/jvq_darwin_amd64.tar.gz) |
| Windows, x86-64 | [jvq_windows_amd64.zip](https://github.com/just-very-quickly/jvq/releases/latest/download/jvq_windows_amd64.zip) |
| Windows, ARM64 | [jvq_windows_arm64.zip](https://github.com/just-very-quickly/jvq/releases/latest/download/jvq_windows_arm64.zip) |

Or with Go: `go install github.com/just-very-quickly/jvq/cmd/jvq@latest`.
jvq needs `git` on the PATH, and whatever your benchmark command needs.

Jenkins, another CI or a laptop work the same way: check out with full
history, then `jvq run`. The exit code is the gate, and `-junit FILE`
writes a JUnit report that most CI systems can show.

## Configuration: jvq.toml

Optional. jvq looks in the current folder and its parents, up to the
repository root.

```toml
# The benchmark command, run in each worktree. Default in a Go module:
# go test -run=NONE -bench=. -benchmem ./...
command = "./bench.test -test.run=NONE -test.bench=. -test.benchmem"
build   = "go test -c -o bench.test ./parser"   # once per side, optional
adapter = "go"          # go, json, time, criterion, google-benchmark, pytest-benchmark, vitest
# output = "results.json"  # a file the command writes its results to, if not stdout
# size = ["bin/app"]       # files whose size is gated, measured after the build
# history = "jvq-history.jsonl"  # add a line per run to this file
history_keep = 1000     # how many runs the history file keeps

rounds     = 10         # per side, to start with
max_rounds = 30         # per side, at most, while undecided
warmup     = true
confidence = 0.95
timeout    = "10m"      # per command run
fail_on_inconclusive = false

[budget]
time   = "5%"           # how much slower is allowed; always a percentage
bytes  = "10%"          # B/op: a percentage, an amount, or "off" (default)
allocs = "0"            # allocs/op: any increase fails; default "off"
size   = "1%"           # files listed in size = [...]; default "1%"

[budget.bench."BenchmarkParse*"]   # tighter for the hot path
time = "2%"

[ignore]
bench = ["BenchmarkNetwork*"]      # too noisy to gate
```

`*` in a name matches anything, slashes included, so `BenchmarkParse*`
covers its sub-benchmarks. `jvq check` reads the file and prints what it
understood. Building once with `go test -c` and running the test binary is
faster than `go test` each round, and keeps compile time out of the
measurement.

## Command line

```
jvq run [flags] [-- command]
  -base REF              compare against this commit
  -head REF              the commit to check (default HEAD)
  -config FILE           config file
  -rounds N              rounds per side to start with (default 10)
  -max-rounds N          most rounds per side while undecided (default 30)
  -build CMD             command run once per side first
  -adapter NAME          how to read results: go, json, time, criterion,
                         google-benchmark, pytest-benchmark or vitest
  -output FILE           read results from this file the command writes
  -timeout D             limit per command run, such as 10m
  -json FILE             also write the report as JSON
  -markdown FILE         also write the report as Markdown
  -junit FILE            also write the report as JUnit XML (GitLab, Jenkins)
  -history FILE          add a line for this run to a history file
  -base-may-fail         if base doesn't build or run, pass with nothing compared
  -fail-on-inconclusive  exit 1 when the result is inconclusive
  -no-warmup             skip the warm-up runs
  -keep                  keep the worktrees, for debugging
  -q                     no progress lines
jvq compare [flags] OLD NEW
  -adapter NAME          any adapter but time (default: jvq.toml's adapter,
                         else json for .json files, else go)
  -config FILE           config file, for budgets and ignores
  -json FILE, -markdown FILE, -fail-on-inconclusive    as for run
jvq history [flags] FILE
  -n N                   only the newest N runs
  -bench PATTERN         only matching benchmarks
  -metric NAME           only this metric
  -fail-over PCT         exit 1 if anything has moved more than this in all
  -markdown FILE         also write the table as Markdown
jvq check [-config FILE]
jvq version
```

Exit codes: 0 pass (inconclusive too, by default), 1 regression, 2 error.
jvq compares commits, so uncommitted changes aren't measured.
[docs/reference.md](docs/reference.md) has every detail, including the JSON
report's fields.

## What it doesn't do (yet)

- Other harnesses' own formats beyond the four above. The `time` and
  `json` adapters cover the rest.
- Benchmarks that take minutes per run: jvq runs the command 20 to 60
  times. Narrow the command to the benchmarks that matter.
- Submodules aren't initialised in the worktrees.

## Tests

```sh
go test ./...                    # unit tests and the binary on fixture repos
sh scripts/record.sh             # everything, written to test/results/
```

The fixture tests run the real binary on throwaway git repositories: a 50%
slowdown caught, an unchanged head passed, one extra allocation caught, a
renamed benchmark, a base that doesn't build, a shallow clone, an interrupt
and a SIGKILL mid-run with nothing left behind. CI runs them on Linux, macOS
and Windows.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE). jvq uses
only the Go standard library; [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md)
has its license. A [SoftDevStudio.com](https://softdevstudio.com) project.
Site: [jvq.net](https://jvq.net).
