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

**Status: v0.2.0.** Go benchmark output, whole-command timing, a small JSON
format, `jvq compare` for two saved outputs, time, bytes and allocation
budgets, adaptive rounds, a GitHub Action, Linux, macOS and Windows. Rust,
C++, Python and JavaScript benchmark tools come next. jvq is open source
under the Apache License 2.0.

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
   over, which is part of why a change of exactly +5% was flagged in 1 to 5
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
| 5% more work | 50 | 1 (2%) | no regression, at the budget's edge |
| 10% more work | 50 | 34 (68%) | regression |
| 20% more work | 50 | 50 (100%) | regression |
| 50% more work | 50 | 50 (100%) | regression |
| one more allocation per op | 100 | 100 (100%) | regression |
| no change, allocations gated | 100 | 0 (0%) | no regression |

The runs that weren't flagged passed or came back inconclusive. Clear cases
took the 10 starting rounds; close ones used more, up to 30 per side, and a
time run took 1.4 to 3.5 seconds on this fixture. On a machine this noisy,
a 10% slowdown is close to what 10 to 30 rounds can settle: an earlier
recording on the same kind of VM, at 8.9% noise, caught 42 of 50.

On a GitHub-hosted runner (ubuntu-latest, 4 cores) the same suite saw only
0.4% run-to-run noise, and every clear case was decided in the first 10
rounds: all 200 no-change runs and all 50 at +2% passed, and every run at
+10%, +20% and +50% was caught. A change of exactly the budget, +5%, was
flagged in 5 of 50 runs. [test/results/github/](test/results/github/accuracy.md)
has that recording.

Near the budget, jvq would rather say "can't tell" than guess. That's the
design: a gate that cries wolf gets switched off.

### Harder settings

[test/results/scenarios.md](test/results/scenarios.md) records three more
situations, each with a known answer, on the same two machines:

| Setting | Cloud VM (2 cores) | GitHub runner (4 cores) |
|---|---|---|
| A neighbour keeping half the CPUs busy and churning 512 MB: no change, 100 runs | 0 false alarms | 0 false alarms |
| The same neighbour, 10% more work, 50 runs | 43 caught, 7 inconclusive | 50 caught |
| Every run 2% slower than the one before, no change, base and head taking turns (ABBA), 50 runs | 0 false alarms | 0 false alarms |
| The same drift with all of base run before all of head, 50 runs | 37 false alarms | 50 false alarms |
| A Python script timed whole (time adapter): no change, 60 runs; 20% more work, 30 runs | 0 false alarms; 26 caught | 0 false alarms; 30 caught |
| The same script reporting its own timings (json adapter): no change, 60 runs; 20% more work, 30 runs | 0 false alarms; 30 caught | 0 false alarms; 30 caught |

The drift rows are why jvq takes turns. A machine that slows down during a
job makes whatever runs second look slower; run in sequence, that faked a
regression in most runs, while taking turns faked none. Taking turns can't
remove the extra spread such drift causes, though: most of those runs came
back inconclusive rather than pass. The Python rows at 10% more work, where
the VM caught about 4 in 10 and the GitHub runner all 30, are in the file.

These recordings were made from the 0.2.0 code (commit 82bd6c5 on the VM,
b32c46d on GitHub, which differ only in a test) just before its version
number was raised, so the files name it 0.1.0.

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
      - uses: just-very-quickly/jvq@v0.2.0
        with:
          command: go test -run=NONE -bench=. -benchmem ./...
```

The action downloads the release binary for the runner, checks its SHA256
against the release's SHA256SUMS, and runs `jvq run`. It needs no token and
no extra permissions; the report goes to the job summary. Inputs: `command`,
`base`, `config`, `args` (more flags), `working-directory` and `version`.

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

GitLab CI, Jenkins or a laptop work the same way: check out with full
history, then `jvq run`. The exit code is the gate.

## Configuration: jvq.toml

Optional. jvq looks in the current folder and its parents, up to the
repository root.

```toml
# The benchmark command, run in each worktree. Default in a Go module:
# go test -run=NONE -bench=. -benchmem ./...
command = "./bench.test -test.run=NONE -test.bench=. -test.benchmem"
build   = "go test -c -o bench.test ./parser"   # once per side, optional
adapter = "go"          # go, json or time
# output = "results.json"  # a file the command writes its results to, if not stdout

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
  -adapter NAME          how to read results: go, json or time
  -output FILE           read results from this file the command writes
  -timeout D             limit per command run, such as 10m
  -json FILE             also write the report as JSON
  -markdown FILE         also write the report as Markdown
  -base-may-fail         if base doesn't build or run, pass with nothing compared
  -fail-on-inconclusive  exit 1 when the result is inconclusive
  -no-warmup             skip the warm-up runs
  -keep                  keep the worktrees, for debugging
  -q                     no progress lines
jvq compare [flags] OLD NEW
  -adapter NAME          go or json (default: json for .json files, else go)
  -config FILE           config file, for budgets and ignores
  -json FILE, -markdown FILE, -fail-on-inconclusive    as for run
jvq check [-config FILE]
jvq version
```

Exit codes: 0 pass (inconclusive too, by default), 1 regression, 2 error.
jvq compares commits, so uncommitted changes aren't measured.
[docs/reference.md](docs/reference.md) has every detail, including the JSON
report's fields.

## What it doesn't do (yet)

- Other harnesses' own formats: Rust criterion, Google Benchmark,
  pytest-benchmark and vitest are planned for v0.3. Until then, the `time`
  and `json` adapters cover any of them.
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
