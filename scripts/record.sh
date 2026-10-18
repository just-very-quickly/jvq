#!/bin/sh
# Copyright JVQ.net 2026
# SPDX-License-Identifier: Apache-2.0
#
# Runs every test and writes the results to test/results/, where the numbers
# in the README and on the site come from:
#
#   tests.txt         go test -v ./... (unit tests and the binary on fixture repos)
#   race.txt          the same under the race detector
#   release-plan.txt  the release plan's cases
#   smoke.txt         the smoke check on a release-style static build
#   accuracy.json     the accuracy suite: false alarms and detection rates
#   accuracy.md       its tables
#
# It needs Go, git and Python 3. The accuracy suite takes 20 to 40 minutes;
# set SKIP_ACCURACY=1 to leave the last recording in place.
#
# The accuracy-results branch holds the same suite recorded on a GitHub-hosted
# runner by .github/workflows/accuracy.yml; this script copies it into
# test/results/github/ when the branch can be fetched.
#
#   sh scripts/record.sh
set -eu
cd "$(dirname "$0")/.."
out=test/results
mkdir -p "$out"

echo "record: unit and fixture tests"
go test -count=1 -v ./... > "$out/tests.txt" 2>&1
echo "record: race detector"
go test -race -count=1 ./... > "$out/race.txt" 2>&1
echo "record: release plan cases"
python3 -B -m unittest discover -s scripts -p '*_test.py' > "$out/release-plan.txt" 2>&1
echo "record: smoke check"
bin=$(mktemp -d)
CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "$bin/jvq" ./cmd/jvq
python3 scripts/smoke.py "$bin" > "$out/smoke.txt" 2>&1
rm -rf "$bin"

if [ "${SKIP_ACCURACY:-}" != 1 ]; then
  echo "record: accuracy suite (this takes a while)"
  go run ./test/accuracy -out "$out"
fi

if git fetch -q origin accuracy-results 2> /dev/null; then
  mkdir -p "$out/github"
  for f in accuracy.json accuracy.md progress.txt COMMIT; do
    git show "FETCH_HEAD:github-ubuntu-latest/$f" > "$out/github/$f"
  done
  echo "record: copied the GitHub runner recording ($(cat "$out/github/COMMIT" | cut -c1-10))"
fi
echo "record: done"
