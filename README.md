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

**Status: v0.1.0.** Go's benchmark format, time, bytes and allocation budgets,
adaptive rounds, a GitHub Action, Linux, macOS and Windows. Other benchmark
formats come next. jvq is open source under the Apache License 2.0.

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
   over, which is part of why a +5% change was flagged in 1 of 50 runs.
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
same benchmark binary, run 40 times in a row, spread by 8.9% from run to run
([test/results/noise.json](test/results/noise.json)), against a 5% budget.

| Head | Runs | Flagged as regression | Right answer |
|---|---:|---:|---|
| no change | 200 | 0 (0%) | no regression |
| 2% more work | 50 | 0 (0%) | no regression |
| 5% more work | 50 | 1 (2%) | no regression, at the budget's edge |
| 10% more work | 50 | 42 (84%) | regression |
| 20% more work | 50 | 50 (100%) | regression |
| 50% more work | 50 | 50 (100%) | regression |
| one more allocation per op | 100 | 100 (100%) | regression |
| no change, allocations gated | 100 | 0 (0%) | no regression |

The runs that weren't flagged passed or came back inconclusive. Clear cases
took the 10 starting rounds; close ones used more, up to 30 per side, and a
time run took 1.5 to 3.6 seconds on this fixture.

Near the budget, jvq would rather say "can't tell" than guess. That's the
design: a gate that cries wolf gets switched off.

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
      - uses: just-very-quickly/jvq@v0.1.0
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
adapter = "go"          # the output format; v0.1 reads Go's

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
  -adapter NAME          output format: go
  -timeout D             limit per command run, such as 10m
  -json FILE             also write the report as JSON
  -markdown FILE         also write the report as Markdown
  -base-may-fail         if base doesn't build or run, pass with nothing compared
  -fail-on-inconclusive  exit 1 when the result is inconclusive
  -no-warmup             skip the warm-up runs
  -keep                  keep the worktrees, for debugging
  -q                     no progress lines
jvq check [-config FILE]
jvq version
```

Exit codes: 0 pass (inconclusive too, by default), 1 regression, 2 error.
jvq compares commits, so uncommitted changes aren't measured.
[docs/reference.md](docs/reference.md) has every detail, including the JSON
report's fields.

## What it doesn't do (yet)

- Formats other than Go's benchmark output. Timing any command (`time`), a
  small JSON format, Rust criterion, Google Benchmark, pytest-benchmark and
  vitest are planned.
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
