# flakydemo

A tiny Go module for testing jvq's flaky gate on real `go test -json`
output (cmd/jvq/flaky_test.go and the action-test workflow). It's its own
module, so the repository's `go test ./...` leaves it out.

`JVQ_DEMO` picks how TestFlaky behaves:

- empty: it passes;
- `flaky`: it fails on its first attempt in the process and passes after,
  so `go test -count=2` gives a failed and a passed attempt;
- `crash`: it panics, which stops the package, so TestB never runs.

`testdata-fail.json` is a saved run in which TestA failed, for checking
that a failure outside quarantine fails the action's step.
