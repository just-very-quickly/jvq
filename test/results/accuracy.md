# jvq accuracy

Recorded 2026-10-19 with jvq 0.1.0 (go1.24.7, linux/amd64) on Intel(R) Xeon(R) Processor @ 2.10GHz, 2 CPUs (linux/amd64). Each run is a full `jvq run` with the default settings: two git worktrees, a build per side, a warm-up, then 10 interleaved rounds per side of a 20ms Go benchmark, adding rounds up to 30 while undecided, judged against the 5% time budget at 95% confidence. Total time 19m21s.

| Test | Injected | Runs | Regression | Pass | Inconclusive | Median measured change | Median rounds | Right answer |
|---|---|---:|---:|---:|---:|---:|---:|---|
| no change (A/A) | 0% | 200 | 0 (0.0%) | 180 | 20 | -0.6% | 12 | no regression |
| slowdown (A/B) | +2% | 50 | 0 (0.0%) | 23 | 27 | +1.8% | 30 | no regression (within the 5% budget) |
| slowdown (A/B) | +5% | 50 | 1 (2.0%) | 5 | 44 | +4.8% | 30 | no regression (within the 5% budget) |
| slowdown (A/B) | +10% | 50 | 34 (68.0%) | 0 | 16 | +10.2% | 20 | regression |
| slowdown (A/B) | +20% | 50 | 50 (100.0%) | 0 | 0 | +19.6% | 10 | regression |
| slowdown (A/B) | +50% | 50 | 50 (100.0%) | 0 | 0 | +49.4% | 10 | regression |
| allocations | +1 alloc/op | 100 | 100 (100.0%) | 0 | 0 | - | 3 | regression |
| allocations, no change | 0 | 100 | 0 (0.0%) | 100 | 0 | - | 3 | no regression |

"Injected" is the extra work per op in the fixture benchmark (a CPU loop whose length is set by a constant). Regression means jvq exited 1; pass and inconclusive both exit 0 by default.
