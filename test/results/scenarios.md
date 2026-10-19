# jvq in harder settings

Recorded 2026-10-19 with jvq 0.1.0 (go1.24.7, linux/amd64) on Intel(R) Xeon(R) Processor @ 2.10GHz, 2 CPUs (linux/amd64). Total time 34m45s. The time budget is the default 5%, at 95% confidence.

- **noisy**: the Go fixture benchmark (20 ms a round, 10 rounds to start, up to 30), beside a process keeping 1 of the 2 CPUs busy and churning through 512 MB.
- **drift**: the fixture program with the json adapter, each run 2% slower than the one before, 10 rounds a side, no warm-up, no added rounds.
- **python**: a Python script running a 800000-round loop: timed whole by the time adapter, or timing 5 batches of a tenth of it with the json adapter; 10 rounds to start, up to 30.

| Scenario | Head | Runs | Regression | Pass | Inconclusive | Median measured change | Median rounds | Right answer |
|---|---|---:|---:|---:|---:|---:|---:|---|
| noisy | no change | 100 | 0 (0.0%) | 82 | 18 | -0.1% | 16 | no regression |
| noisy | +10% work | 50 | 43 (86.0%) | 0 | 7 | +10.5% | 12 | regression |
| drift | ABBA order | 50 | 0 (0.0%) | 1 | 49 | -0.3% | 10 | no regression |
| drift | sequential order | 50 | 37 (74.0%) | 0 | 13 | +18.4% | 10 | no regression |
| python, time adapter | no change | 60 | 0 (0.0%) | 32 | 28 | -1.0% | 30 | no regression |
| python, time adapter | +10% work | 30 | 11 (36.7%) | 0 | 19 | +10.7% | 30 | regression |
| python, time adapter | +20% work | 30 | 26 (86.7%) | 0 | 4 | +20.0% | 12 | regression |
| python, json adapter | no change | 60 | 0 (0.0%) | 34 | 26 | -0.7% | 26 | no regression |
| python, json adapter | +10% work | 30 | 12 (40.0%) | 0 | 18 | +10.3% | 30 | regression |
| python, json adapter | +20% work | 30 | 30 (100.0%) | 0 | 0 | +21.2% | 14 | regression |

In the drift rows head is the same as base; only the order of the runs differs. In the Python rows, "+10% work" is 10% more rounds of the loop: with the time adapter the whole command also includes Python's start-up, so its measured change is a little smaller.
