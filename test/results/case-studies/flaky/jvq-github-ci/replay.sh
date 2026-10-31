#!/bin/sh
# Feeds the rebuilt CI runs through jvq flaky, one history per OS and test
# step, oldest run first, with each run's real commit. Writes output/.
#
#   JVQ=/path/to/jvq sh replay.sh
set -u
here=$(cd "$(dirname "$0")" && pwd)
JVQ=${JVQ:-jvq}
out=$here/output
rm -rf "$out" && mkdir -p "$out/json"
"$JVQ" version >"$out/version.txt"
for d in "$here"/junit/*/; do
	h=$(basename "$d")
	mkdir -p "$out/history/$h"
	hist=jvq-tests.jsonl
	: >"$out/$h.txt"
	cd "$out/history/$h" || exit 2
	for f in "$d"*.xml; do
		b=$(basename "$f" .xml)
		sha=${b#*-}
		{
			echo "=== run ${b%%-*}, commit $(printf %.7s "$sha")"
			"$JVQ" flaky -history "$hist" -commit "$sha" -json "$out/json/$h-$b.json" "$f"
			echo "exit $?"
		} >>"$out/$h.txt" 2>&1
	done
	cd "$here" || exit 2
done
python3 -I "$here/summary.py" >"$out/summary.md"
cat "$out/summary.md"
