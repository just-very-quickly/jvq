#!/bin/sh
# Runs every recipe in the site's "use examples" post for real, on a small
# demo project made here, and saves what each step printed to output/.
#
#   JVQ=/path/to/jvq sh run.sh
#
# JVQ is the released binary (default: jvq on PATH). Needs git, go,
# python3 with pytest (and pytest-rerunfailures for step 3b), and jq.
# The flaky tests here really are random, so a rerun gives other numbers;
# the files in output/ are the run the post quotes.
set -u
here=$(cd "$(dirname "$0")" && pwd)
JVQ=${JVQ:-jvq}
out=$here/output
rm -rf "$out" && mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
"$JVQ" version >"$out/00-version.txt"
python3 -m pytest --version >>"$out/00-version.txt" 2>&1
python3 -c "import importlib.metadata as m; print(\"pytest-rerunfailures\", m.version(\"pytest-rerunfailures\"))" >>"$out/00-version.txt" 2>&1
go version >>"$out/00-version.txt"

# ---------------------------------------------------------------- the demo
demo=$work/shop
mkdir -p "$demo/tests"
cd "$demo"
git init -q -b main . && git config user.email demo@example.com && git config user.name demo
cat >tests/test_cart.py <<'EOF'
import random

def test_total():
    assert sum([3, 4]) == 7

def test_discount():
    assert round(100 * 0.9, 2) == 90.0

def test_receipt_lines():
    assert "\n".join(["a", "b"]).count("\n") == 1

def test_checkout_times_out():
    # Stands in for a call to a slow service: fails about one try in three.
    assert random.random() > 0.35
EOF
echo "v1" >VERSION
printf 'junit/\n.pytest_cache/\n__pycache__/\n*.json\n*.jsonl\n' >.gitignore
git add -A && git commit -q -m "shop v1"

# pytest with up to 2 retries of what failed: one JUnit file per attempt.
pytest_run() {
	rm -rf junit && mkdir junit
	python3 -m pytest -q --junitxml=junit/attempt1.xml >/dev/null 2>&1 ||
		python3 -m pytest -q --lf --junitxml=junit/attempt2.xml >/dev/null 2>&1 ||
		python3 -m pytest -q --lf --junitxml=junit/attempt3.xml >/dev/null 2>&1
	return 0
}

# ------------------------------------------- 1. pytest JUnit, run after run
# A new commit for each CI run, as on a busy main branch, until the flaky
# test is in quarantine (or 15 commits).
: >"$out/01-pytest-runs.txt"
n=0
while [ $n -lt 15 ]; do
	n=$((n + 1))
	echo "v1.$n" >VERSION && git commit -q -am "change $n"
	pytest_run
	{
		echo "=== CI run $n, commit $(git rev-parse --short HEAD), files: $(ls junit | tr '\n' ' ')"
		"$JVQ" flaky -history jvq-tests.jsonl -json "$work/q.json" junit/
		echo "exit $?"
	} >>"$out/01-pytest-runs.txt" 2>&1
	jq -e '.result.in_quarantine > 0' "$work/q.json" >/dev/null 2>&1 && break
done
echo "$n CI runs" >>"$out/01-pytest-runs.txt"

# ------------------------------------------------- 2. reading the quarantine
{
	echo '$ jvq flaky -history jvq-tests.jsonl'
	"$JVQ" flaky -history jvq-tests.jsonl
	echo "exit $?"
} >"$out/02-quarantine-list.txt" 2>&1
pytest_run
"$JVQ" flaky -history jvq-tests.jsonl -no-write -json report.json junit/ >/dev/null 2>&1
{
	echo '$ jq -r '"'"'.result.tests_of_note[] | select(.quarantined) | "\(.test)  since \(.quarantined_since)  flaky commits \(.flaky_commits)"'"'"' report.json'
	jq -r '.result.tests_of_note[] | select(.quarantined) | "\(.test)  since \(.quarantined_since)  flaky commits \(.flaky_commits)"' report.json
} >"$out/02-quarantine-jq.txt" 2>&1
cp jvq-tests.jsonl "$work/quarantined.jsonl"

# ----------------------------------------------- 3a. why two retries
# The flaky test now breaks for real: every attempt fails. Judge one run
# against the history with no retries, one retry and two retries.
sed -i 's/random.random() > 0.35/False  # a real bug now/' tests/test_cart.py
git commit -q -am "break checkout for real"
for r in 0 1 2; do
	rm -rf junit && mkdir junit
	python3 -m pytest -q --junitxml=junit/attempt1.xml >/dev/null 2>&1
	k=1
	while [ $k -le $r ]; do
		k=$((k + 1))
		python3 -m pytest -q --lf --junitxml=junit/attempt$k.xml >/dev/null 2>&1
	done
	{
		echo "=== $r retries, files: $(ls junit | tr '\n' ' ')"
		"$JVQ" flaky -history "$work/quarantined.jsonl" -no-write junit/
		echo "exit $?"
	} >>"$out/03a-retries.txt" 2>&1
done
sed -i 's/False  # a real bug now/random.random() > 0.35/' tests/test_cart.py
git commit -q -am "undo the break"

# ------------------------------------------- 3b. pytest-rerunfailures' JUnit
# Does its JUnit keep the failed attempts? A test that fails once, then passes.
mkdir -p "$work/rerun/tests" && cd "$work/rerun"
cat >tests/test_once.py <<'EOF'
import os

def test_fails_first_time():
    mark = os.path.join(os.path.dirname(__file__), ".tried")
    if not os.path.exists(mark):
        open(mark, "w").close()
        assert False, "first try"
EOF
{
	echo '$ pytest --reruns 2 --junitxml=reruns.xml'
	python3 -m pytest -q -p no:cacheprovider --reruns 2 --junitxml=reruns.xml 2>&1 | tail -1
	echo '--- reruns.xml'
	cat reruns.xml
	echo
	echo '$ jvq flaky -history - reruns.xml'
	"$JVQ" flaky -history - reruns.xml
	echo "exit $?"
} >"$out/03b-pytest-rerunfailures.txt" 2>&1
cd "$demo"

# ---------------------------------------------- 4. releasing a fixed test
# The fix: no more randomness. Then CI runs until the test leaves.
sed -i 's/random.random() > 0.35/True  # fixed: the slow call is mocked/' tests/test_cart.py
git commit -q -am "fix checkout test"
: >"$out/04-release-runs.txt"
m=0
while [ $m -lt 40 ]; do
	m=$((m + 1))
	echo "v2.$m" >VERSION && git commit -q -am "after fix $m"
	pytest_run
	"$JVQ" flaky -history jvq-tests.jsonl -json "$work/r.json" junit/ >"$work/r.txt" 2>&1
	if jq -e '[.result.tests_of_note[] | select(.event == "released")] | length > 0' "$work/r.json" >/dev/null; then
		{ echo "=== run $m after the fix"; cat "$work/r.txt"; } >>"$out/04-release-runs.txt"
		break
	fi
	[ $m -eq 1 ] && { echo "=== run 1 after the fix"; cat "$work/r.txt"; } >>"$out/04-release-runs.txt"
done
echo "released after $m passing runs" >>"$out/04-release-runs.txt"

# The shortcut: drop the test from the history so its flaky commits go.
id=$(jq -r '.result.tests_of_note[] | select(.quarantined) | .test' report.json | head -1)
mkdir -p "$work/shortcut" && cp "$work/quarantined.jsonl" "$work/shortcut/before.jsonl"
(
	cd "$work/shortcut"
	echo "\$ jq -c --arg id '$id' 'if .tests then del(.tests[\$id]) else . end' before.jsonl > jvq-tests.jsonl"
	jq -c --arg id "$id" 'if .tests then del(.tests[$id]) else . end' before.jsonl >jvq-tests.jsonl
	echo '$ jvq flaky -history jvq-tests.jsonl'
	"$JVQ" flaky -history jvq-tests.jsonl
	echo "exit $?"
) >"$out/04-release-jq.txt" 2>&1

# --------------------------------- 5. Go: go test -json with reruns in one file
# gotestsum --rerun-fails=2 --jsonfile writes the go test -json stream of
# the first run and then of each rerun into one file. The same stream,
# written by go test directly:
g=$work/gomod && mkdir -p "$g" && cd "$g"
cat >go.mod <<'EOF'
module example.com/shop

go 1.22
EOF
cat >shop_test.go <<'EOF'
package shop

import (
	"math/rand"
	"testing"
)

func TestTotal(t *testing.T) {
	if 3+4 != 7 {
		t.Fatal("sum")
	}
}

func TestCheckoutTimesOut(t *testing.T) {
	if rand.Float64() < 0.35 { // stands in for a slow service
		t.Fatal("checkout timed out")
	}
}
EOF
: >"$out/05-go-reruns.txt"
for try in 1 2 3 4 5 6; do
	go test -json -count=1 ./... >go-test.json 2>/dev/null
	for k in 2 3; do
		grep -q '"Action":"fail","Package":"example.com/shop","Test":"TestCheckoutTimesOut"' go-test.json || break
		# only rerun if the latest attempt failed
		last=$(grep '"Test":"TestCheckoutTimesOut"' go-test.json | grep -E '"Action":"(pass|fail)"' | tail -1)
		echo "$last" | grep -q '"Action":"fail"' || break
		go test -json -count=1 -run '^TestCheckoutTimesOut$' ./... >>go-test.json 2>/dev/null
	done
	if grep -q '"Action":"fail","Package":"example.com/shop","Test":"TestCheckoutTimesOut"' go-test.json; then
		{
			echo "=== run $try: TestCheckoutTimesOut failed at least once"
			echo '$ jvq flaky -history - go-test.json'
			"$JVQ" flaky -history - go-test.json
			echo "exit $?"
		} >>"$out/05-go-reruns.txt" 2>&1
		break
	fi
	echo "=== run $try: no failure, nothing to show" >>"$out/05-go-reruns.txt"
done

# ---------------------------- 6. the GitHub Actions steps, outside GitHub
# The workflow's jvq step, with the variables GitHub sets: a pull request
# reads the history and doesn't write it; a push to main writes it.
cd "$demo"
cp "$work/quarantined.jsonl" jvq-tests.jsonl
pytest_run
sha=$(git rev-parse HEAD)
for ref in refs/pull/7/merge refs/heads/main; do
	sum=$work/summary-$(basename "$ref").md
	: >"$sum"
	before=$(wc -l <jvq-tests.jsonl)
	nw=""
	[ "$ref" = refs/heads/main ] || nw="-no-write"
	{
		echo "=== GITHUB_REF=$ref: jvq flaky -history jvq-tests.jsonl $nw junit/"
		GITHUB_ACTIONS=true GITHUB_REF=$ref GITHUB_SHA=$sha GITHUB_STEP_SUMMARY=$sum \
			"$JVQ" flaky -history jvq-tests.jsonl $nw junit/
		echo "exit $?"
		echo "history lines before $before, after $(wc -l <jvq-tests.jsonl)"
		echo "--- job summary"
		cat "$sum"
	} >>"$out/06-github-steps.txt" 2>&1
done
python3 -I -c 'import yaml, sys; yaml.safe_load(open(sys.argv[1])); print("workflow.yml parses")' "$here/workflow.yml" >"$out/06-yaml.txt" 2>&1 ||
	echo "PyYAML not installed; YAML not checked" >"$out/06-yaml.txt"
echo done
