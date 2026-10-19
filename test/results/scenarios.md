# jvq in harder settings

Recorded 2026-10-19 with jvq 0.3.0 (go1.24.7, linux/amd64) on Intel(R) Xeon(R) Processor @ 2.10GHz, 2 CPUs (linux/amd64). Total time 1h20m22s. The time budget is the default 5%, at 95% confidence.

- **noisy**: the Go fixture benchmark (20 ms a round, 10 rounds to start, up to 30), beside a process keeping 1 of the 2 CPUs busy and churning through 512 MB.
- **drift**: the fixture program with the json adapter, each run 2% slower than the one before, 10 rounds a side, no warm-up, no added rounds.
- **python**: a Python script running a 800000-round loop: timed whole by the time adapter, or timing 5 batches of a tenth of it with the json adapter; 10 rounds to start, up to 30.
- **harness**: the real harnesses through jvq run with their adapters: pytest-benchmark, Google Benchmark (its Python bindings), vitest bench and Rust's criterion, each timing a loop whose length is set per commit; 10 rounds to start, up to 30.

| Scenario | Head | Runs | Regression | Pass | Inconclusive | Median measured change | Median rounds | Right answer |
|---|---|---:|---:|---:|---:|---:|---:|---|
| noisy | no change | 100 | 0 (0.0%) | 89 | 11 | -0.3% | 12 | no regression |
| noisy | +10% work | 50 | 38 (76.0%) | 0 | 12 | +10.4% | 20 | regression |
| drift | ABBA order | 50 | 0 (0.0%) | 1 | 49 | -0.1% | 10 | no regression |
| drift | sequential order | 50 | 44 (88.0%) | 0 | 6 | +19.0% | 10 | no regression |
| python, time adapter | no change | 60 | 0 (0.0%) | 28 | 32 | -1.3% | 30 | no regression |
| python, time adapter | +10% work | 30 | 8 (26.7%) | 0 | 22 | +9.7% | 30 | regression |
| python, time adapter | +20% work | 30 | 28 (93.3%) | 0 | 2 | +21.7% | 10 | regression |
| python, json adapter | no change | 60 | 0 (0.0%) | 35 | 25 | -0.7% | 24 | no regression |
| python, json adapter | +10% work | 30 | 20 (66.7%) | 0 | 10 | +10.6% | 16 | regression |
| python, json adapter | +20% work | 30 | 29 (96.7%) | 0 | 1 | +21.8% | 12 | regression |
| harness, pytest-benchmark | no change | 10 | 1 (10.0%) | 2 | 7 | +1.7% | 30 | no regression |
| harness, pytest-benchmark | +20% work | 10 | 10 (100.0%) | 0 | 0 | +21.5% | 14 | regression |
| harness, google-benchmark | no change | 10 | 0 (0.0%) | 4 | 6 | -2.2% | 30 | no regression |
| harness, google-benchmark | +20% work | 10 | 8 (80.0%) | 0 | 2 | +23.4% | 10 | regression |
| harness, vitest | no change | 10 | 0 (0.0%) | 9 | 1 | +1.1% | 18 | no regression |
| harness, vitest | +20% work | 10 | 10 (100.0%) | 0 | 0 | +22.0% | 10 | regression |
| harness, criterion | no change | 10 | 0 (0.0%) | 8 | 2 | +0.4% | 18 | no regression |
| harness, criterion | +20% work | 10 | 10 (100.0%) | 0 | 0 | +20.5% | 10 | regression |

In the drift rows head is the same as base; only the order of the runs differs. In the Python and harness rows, "+10% work" or "+20% work" is that many more rounds of the loop: with the time adapter the whole command also includes Python's start-up, so its measured change is a little smaller.
