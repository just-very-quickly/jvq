#!/bin/sh
# Feeds the recorded runs through jvq flaky, one history per CI step
# (plain and -race), oldest commit first, with each run's real commit.
# Writes output/. With RAW set, also replays the raw files and checks
# that every verdict and attempt string matches the trimmed ones.
#
#   JVQ=/path/to/jvq sh replay.sh
set -u
here=$(cd "$(dirname "$0")" && pwd)
JVQ=${JVQ:-jvq}
replay() { # SRC OUT
	src=$1 out=$2
	rm -rf "$out" && mkdir -p "$out/json"
	for sd in "$src"/*/; do
		step=$(basename "$sd")
		mkdir -p "$out/history/$step"
		: >"$out/$step.txt"
		for rd in "$sd"*/; do
			run=$(basename "$rd")
			short=${run#*-}
			sha=$(git -C "$here" rev-parse "$short^{commit}" 2>/dev/null || echo "$short")
			(
				cd "$out/history/$step" || exit 2
				echo "=== $run, files: $(cd "$rd" && ls *.json | tr '\n' ' ')"
				"$JVQ" flaky -history jvq-tests.jsonl -commit "$sha" -json "$out/json/$step-$run.json" "$rd"
				echo "exit $?"
			) >>"$out/$step.txt" 2>&1
		done
	done
}
"$JVQ" version >"$here/version.txt"
replay "$here/runs" "$here/output"
if [ -n "${RAW:-}" ]; then
	tmp=$(mktemp -d)
	replay "$RAW" "$tmp"
	python3 -I - "$here/output/json" "$tmp/json" >"$here/output/trim-check.txt" <<'PY'
import json, os, sys
a, b = sys.argv[1], sys.argv[2]
bad = 0
for n in sorted(os.listdir(a)):
    ra, rb = json.load(open(os.path.join(a, n)))["result"], json.load(open(os.path.join(b, n)))["result"]
    ka = [(t["test"], t["attempts"], t["status"]) for t in ra.get("tests_of_note") or []]
    kb = [(t["test"], t["attempts"], t["status"]) for t in rb.get("tests_of_note") or []]
    same = ra["verdict"] == rb["verdict"] and ra["tests"] == rb["tests"] and ka == kb
    bad += not same
    print(n, "same" if same else "DIFFERENT")
print("runs that differ:", bad)
PY
	rm -rf "$tmp"
	tail -1 "$here/output/trim-check.txt"
fi
python3 -I "$here/summary.py" >"$here/output/summary.md"
cat "$here/output/summary.md"
