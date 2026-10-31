# jvq flaky test gate

Recorded 2026-10-31 with jvq 0.7.0 (go1.24.7, linux/amd64) on Intel(R) Xeon(R) Processor @ 2.80GHz, 2 CPUs. Total time 11m5s.

Simulated CI histories with a known truth. Every run writes real result files, half the tests as go test -json and half as JUnit XML, and runs `jvq flaky` with the default settings and a history file kept between runs.

## A suite with flaky tests

10 trials of 300 commits each (3000 commits in all). The suite has 200 stable tests and 5 flaky ones, failing 2%, 5%, 10%, 20% and 30% of their attempts; no retries. When a run is red, it's re-run once on the same commit, as people do.

| | Without quarantine | With jvq flaky |
|---|---:|---:|
| First runs that were red | 1611 of 3000 (53.7%) | 437 (14.6%) |
| Red first runs in the last 100 commits of each trial | 556 of 1000 (55.6%) | 96 (9.6%) |
| Commits still red after one re-run | | 103 |
| Stable tests ever quarantined | | 0 |
| Runs that ended in an error (exit 2) | | 0 |

| Flaky test fails | First runs it failed | Of those, runs it blocked | Trials in quarantine at the end | Median commit it went in |
|---:|---:|---:|---:|---:|
| 2% of attempts | 76 | 49 | 0 of 10 | 45 |
| 5% of attempts | 143 | 57 | 5 of 10 | 26 |
| 10% of attempts | 284 | 51 | 10 of 10 | 20 |
| 20% of attempts | 600 | 88 | 9 of 10 | 5 |
| 30% of attempts | 948 | 231 | 9 of 10 | 4 |

A flaky test blocks until it has failed and passed on two commits (here: a red run and its green re-run). The rarer its failures, the longer that takes.

## A quarantined test breaks for real, no retries

50 trials. A test failing 30% of its attempts runs until it's in quarantine and five runs more (a red run is re-run once on the same commit); then it fails every attempt, and so does a stable test beside it (the control).

| | Trials |
|---|---:|
| In quarantine before breaking | 50 |
| Broken test blocked in run 1 after breaking | 3 |
| Broken test blocked in run 2 after breaking | 12 |
| Broken test blocked in run 3 after breaking | 35 |
| Never blocked (within 10 runs) | 0 |
| Stable test blocked in the first run | 50 |
| Runs before the break where the quarantined flaky test failed every attempt and blocked as broken | 1 of 705 |

## A quarantined test breaks for real, 2 retries

50 trials. A test failing 30% of its attempts runs until it's in quarantine and five runs more (a red run is re-run once on the same commit); then it fails every attempt, and so does a stable test beside it (the control). Each failed test is run 2 more times in the same run.

| | Trials |
|---|---:|
| In quarantine before breaking | 50 |
| Broken test blocked in run 1 after breaking | 50 |
| Never blocked (within 10 runs) | 0 |
| Stable test blocked in the first run | 50 |
| Runs before the break where the quarantined flaky test failed every attempt and blocked as broken | 6 of 529 |

## A quarantined test is fixed

10 trials. A test failing 20% of its attempts runs until it's in quarantine (a red run is re-run once on the same commit), then always passes.

| | Trials |
|---|---:|
| In quarantine before the fix | 10 |
| Left quarantine on passing run 29 after the fix | 10 |

The rule is 30 passing runs in a row, and a pass just before the fix (the green re-run that put it in quarantine) counts.

## A damaged and a lost history

10 trials of 200 commits of the flaky suite. At commit 100 the history gets a line of binary junk in the middle and its last line cut in half (a cache restored half way); at commit 150 it's deleted (the cache expired).

| | Result |
|---|---:|
| Runs that ended in an error (exit 2) | 0 |
| Trials with a note about damaged lines | 10 of 10 |
| Trials whose quarantine was the same after the damage | 8 of 10 |
| Tests in quarantine before the damage, all trials | 36 |
| Trials with a note that the history starts again | 10 of 10 |
| Tests back in quarantine 50 commits after the deletion, all trials | 34 |
