# jvq reference

Everything jvq v0.1 does, in one place. The [README](../README.md) is the
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
   - otherwise the merge base of head and the first of: origin's default
     branch (`refs/remotes/origin/HEAD`), `origin/main`, `origin/master`,
     `main`, `master`;
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
Only its standard output is parsed; both outputs are shown if it fails.

| Flag | Default | Meaning |
|---|---|---|
| `-base REF` | see above | the commit to compare against |
| `-head REF` | `HEAD` | the commit to check |
| `-config FILE` | found | config file |
| `-rounds N` | 10 | rounds per side to start with, at least 3 |
| `-max-rounds N` | 30 | most rounds per side while undecided; at or below `-rounds` turns adding rounds off |
| `-build CMD` | none | command run once per side before the rounds |
| `-adapter NAME` | `go` | output format of the command |
| `-timeout D` | `10m` | limit for each command run (Go duration: `90s`, `10m`) |
| `-json FILE` | none | also write the report as JSON |
| `-markdown FILE` | none | also write the report as Markdown |
| `-base-may-fail` | off | if base fails to build or run, print a note and exit 0 with nothing compared |
| `-fail-on-inconclusive` | off | exit 1 when the overall verdict is inconclusive |
| `-no-warmup` | off | skip the warm-up runs |
| `-keep` | off | leave the worktrees in place and print where |
| `-q` | off | no progress lines on stderr |

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
| `adapter` | string | `"go"` | output format; v0.1 has `go` |
| `rounds` | integer, 3 to 1000 | 10 | rounds per side to start with |
| `max_rounds` | integer, 3 to 1000 | 30 | most rounds per side while undecided |
| `warmup` | boolean | true | one thrown-away run per side first |
| `confidence` | number, 0.5 to below 1 | 0.95 | the intervals' confidence level |
| `timeout` | duration string | `"10m"` | limit per command run |
| `fail_on_inconclusive` | boolean | false | inconclusive exits 1 |
| `[budget]` `time` | percentage | `"5%"` | how much slower is allowed |
| `[budget]` `bytes` | percentage, amount or `"off"` | `"off"` | B/op allowance |
| `[budget]` `allocs` | percentage, amount or `"off"` | `"off"` | allocs/op allowance |
| `[budget.bench."PATTERN"]` | the three budget keys | | overrides for matching benchmarks; later tables win |
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
ignored. Each result line is one sample; with `-count=N` a round gives N.

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
in 1 run of 50. Changes well inside the budget, and no change at all, were
never flagged (0 of 50 at +2%, 0 of 200 with no change). Set
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
  "jvq": "0.1.0",
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

`rounds` is how many rounds per side ran, extra ones included. `order` lists
the measured runs in the order they ran. `change`, `ci_lo` and `ci_hi` are
relative (0.1 means +10%); `ci_lo`, `ci_hi` and `p` (the two-sided
Mann-Whitney p-value for no difference) appear for time only. `change` is
left out when base was zero. `note` explains a non-obvious verdict or base.
Medians are in the metric's unit (nanoseconds for time).

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

Check out with `fetch-depth: 0` so the base commit is there.
