# jvq in harder settings

Recorded 2026-10-19 with jvq 0.6.0 (go1.26.9, linux/amd64) on AMD EPYC 9V74 80-Core Processor, 4 CPUs (linux/amd64). Total time 56m16s. The time budget is the default 5%, at 95% confidence.

- **noisy**: the Go fixture benchmark (20 ms a round, 10 rounds to start, up to 30), beside a process keeping 2 of the 4 CPUs busy and churning through 512 MB.
- **drift**: the fixture program with the json adapter, each run 2% slower than the one before, 10 rounds a side, no warm-up, no added rounds.
- **python**: a Python script running a 800000-round loop: timed whole by the time adapter, or timing 5 batches of a tenth of it with the json adapter; 10 rounds to start, up to 30.
- **instructions**: the fixture program under valgrind's cachegrind (the instructions adapter, 1% budget), 5 rounds to start, up to 30; the neighbour case runs beside the noisy neighbour.
- **harness**: the real harnesses through jvq run with their adapters: pytest-benchmark, Google Benchmark (its Python bindings), vitest bench and Rust's criterion, each timing a loop whose length is set per commit; 10 rounds to start, up to 30.

| Scenario | Head | Runs | Regression | Pass | Inconclusive | Median measured change | Median rounds | Right answer |
|---|---|---:|---:|---:|---:|---:|---:|---|
| noisy | no change | 100 | 0 (0.0%) | 100 | 0 | -0.0% | 10 | no regression |
| noisy | +10% work | 50 | 50 (100.0%) | 0 | 0 | +10.0% | 10 | regression |
| drift | ABBA order | 50 | 0 (0.0%) | 0 | 50 | +0.0% | 10 | no regression |
| drift | sequential order | 50 | 50 (100.0%) | 0 | 0 | +18.3% | 10 | no regression |
| python, time adapter | no change | 60 | 0 (0.0%) | 60 | 0 | +0.0% | 10 | no regression |
| python, time adapter | +10% work | 30 | 30 (100.0%) | 0 | 0 | +9.2% | 10 | regression |
| python, time adapter | +20% work | 30 | 30 (100.0%) | 0 | 0 | +18.5% | 10 | regression |
| python, json adapter | no change | 60 | 0 (0.0%) | 60 | 0 | -0.0% | 10 | no regression |
| python, json adapter | +10% work | 30 | 30 (100.0%) | 0 | 0 | +9.9% | 10 | regression |
| python, json adapter | +20% work | 30 | 30 (100.0%) | 0 | 0 | +19.7% | 10 | regression |
| instructions | no change | 20 | 0 (0.0%) | 20 | 0 | +0.0% | 5 | no regression |
| instructions | +0.5% work (within the 1% budget) | 20 | 0 (0.0%) | 20 | 0 | +0.5% | 5 | no regression |
| instructions | +2% work | 20 | 20 (100.0%) | 0 | 0 | +2.0% | 5 | regression |
| instructions | +2% work, beside the neighbour | 20 | 20 (100.0%) | 0 | 0 | +2.0% | 5 | regression |
| instructions | no change, beside the neighbour | 20 | 0 (0.0%) | 20 | 0 | -0.0% | 5 | no regression |
| instructions | no change, 8 goroutines allocating | 20 | 0 (0.0%) | 20 | 0 | -0.0% | 5 | no regression |
| harness, pytest-benchmark | no change | 10 | 0 (0.0%) | 10 | 0 | +0.4% | 10 | no regression |
| harness, pytest-benchmark | +20% work | 10 | 10 (100.0%) | 0 | 0 | +19.9% | 10 | regression |
| harness, google-benchmark | no change | 10 | 0 (0.0%) | 10 | 0 | +0.2% | 10 | no regression |
| harness, google-benchmark | +20% work | 10 | 10 (100.0%) | 0 | 0 | +20.0% | 10 | regression |
| harness, vitest | no change | 10 | 0 (0.0%) | 10 | 0 | +0.0% | 10 | no regression |
| harness, vitest | +20% work | 10 | 10 (100.0%) | 0 | 0 | +20.0% | 10 | regression |
| harness, criterion | no change | 10 | 0 (0.0%) | 10 | 0 | -0.0% | 10 | no regression |
| harness, criterion | +20% work | 10 | 10 (100.0%) | 0 | 0 | +20.0% | 10 | regression |

In the drift rows head is the same as base; only the order of the runs differs. In the Python and harness rows, "+10% work" or "+20% work" is that many more rounds of the loop: with the time adapter the whole command also includes Python's start-up, so its measured change is a little smaller.
