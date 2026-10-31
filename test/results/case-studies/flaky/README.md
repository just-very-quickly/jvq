# Flaky test gate: case studies and use examples

The data behind jvq.net's posts on `jvq flaky`, recorded October 31 2026
with the released v0.7.0 binary (linux/amd64, checked against SHA256SUMS).
Every number in those posts comes from the files here.

- `jvq-github-ci/`: jvq's own test workflow on GitHub, 26 pushes to main
  on Linux, macOS and Windows. `fetch.py` saves the runs, jobs, steps and
  failure annotations from the GitHub API into `api/`; `rebuild.py` turns
  them into one JUnit file per run in `junit/` (the five tests that ever
  failed; `junit/left-out.txt` lists the steps it couldn't use and why);
  `replay.sh` feeds them through jvq, one history per platform and step,
  into `output/`.
- `jvq-suite/`: jvq's own test suite run locally the way its CI runs it on
  Linux (`go test -count=1 ./...`, then `-race`), at its commits up to
  0.7.0, with up to two retries of failed tests. `record.sh` makes the raw
  `go test -json` files, `trim.py` keeps them small in `runs/`, and
  `replay.sh` feeds them through jvq into `output/`.
- `recipes/`: the use examples post. `run.sh` builds a small demo project
  (pytest and Go, each with a test that fails at random about one try in
  three) and runs every recipe, saving what each printed to `output/`.
  `workflow.yml` is the GitHub Actions example.

Rerun with `JVQ=/path/to/jvq` set for each script. The random tests in
`recipes/` give different runs each time; the files here are the ones the
posts quote.
