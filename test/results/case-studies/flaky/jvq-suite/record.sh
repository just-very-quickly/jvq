#!/bin/sh
# Records jvq's own test suite the way its CI runs it on Linux (test.yml:
# `go test -count=1 ./...`, then `go test -race -count=1 ./...`), as
# `go test -json`, at each commit listed below, on whatever machine runs it.
# A failed test is run again up to twice, the way gotestsum --rerun-fails=2
# would, and each retry is its own file in the same run folder.
#
#   REPO=/path/to/a/clone/of/jvq sh record.sh
#
# REPO must be a clone you don't mind being checked out at old commits.
# The raw files go to RAW (default ./raw, not committed: go test -json
# output is large); trim.py makes the copies kept in runs/.
set -u
here=$(cd "$(dirname "$0")" && pwd)
REPO=${REPO:?set REPO to a scratch clone of github.com/just-very-quickly/jvq}
RAW=${RAW:-$here/raw}
STEPS=${STEPS:-"test race"}
# jvq's code commits before 0.7.0 and up to it, oldest first.
COMMITS=${COMMITS:-"d37c25c761d7361a25531dde6289d591718721cd
4bc64c3110fd21ffb4962deab6f59dcbc1aab7e9
e7d1a962e4da784d45fa70c7bf0a746539098d01
5981c1e7c27c7cf9435e85279204459143aa3e87
370cc44e93e9f91aa7187e42114922785f746dec
9cfc77c7e576dbf6caefd1c72804b336b404cf59
a8f1d947a23c2d00ad7cecc73937f838dbbbbf65
9e5bfe9dbde83abe34e420afbab0010c3d27b0a1"}

# Don't disturb a recording of jvq's accuracy suite on the same machine.
wait_quiet() {
	while pgrep -f 'scripts/record.sh|test/accuracy|test/scenarios|test/flaky' >/dev/null 2>&1; do
		echo "waiting: a jvq recording is running" >&2
		sleep 60
	done
}

# failed PKG-and-TEST pairs in a go test -json file, one "pkg<TAB>regex" per
# package: top-level tests that failed, or "." when the package failed with
# no failing test (a build failure or a crash before any test).
failed() {
	python3 -I -c '
import json, sys, collections
tests = collections.OrderedDict(); pkgfail = set()
for line in open(sys.argv[1], encoding="utf-8", errors="replace"):
    try: e = json.loads(line)
    except ValueError: continue
    if not isinstance(e, dict) or e.get("Action") != "fail": continue
    p = e.get("Package", ""); t = e.get("Test")
    if t: tests.setdefault(p, set()).add(t.split("/")[0])
    else: pkgfail.add(p)
for p in sorted(pkgfail | set(tests)):
    ts = tests.get(p)
    print(p + "\t" + ("^(" + "|".join(sorted(ts)) + ")$" if ts else "."))
' "$1"
}

n=0
for c in $COMMITS; do
	n=$((n + 1))
	short=$(printf %.7s "$c")
	git -C "$REPO" checkout -q --detach "$c" || exit 2
	for step in $STEPS; do
		flag=""
		[ "$step" = race ] && flag="-race"
		d="$RAW/$step/$(printf %02d "$n")-$short"
		mkdir -p "$d"
		wait_quiet
		start=$(date +%s)
		(cd "$REPO" && go test $flag -json -count=1 ./... >"$d/attempt1.json" 2>"$d/attempt1.stderr")
		echo "$step $short attempt1 $(($(date +%s) - start))s" >>"$RAW/log.txt"
		prev="$d/attempt1.json"
		for k in 2 3; do
			list=$(failed "$prev")
			[ -n "$list" ] || break
			: >"$d/attempt$k.json"
			echo "$list" | while IFS="	" read -r pkg rx; do
				if [ "$rx" = . ]; then
					(cd "$REPO" && go test $flag -json -count=1 "$pkg" >>"$d/attempt$k.json" 2>>"$d/attempt$k.stderr")
				else
					(cd "$REPO" && go test $flag -json -count=1 -run "$rx" "$pkg" >>"$d/attempt$k.json" 2>>"$d/attempt$k.stderr")
				fi
			done
			echo "$step $short attempt$k: $(echo "$list" | tr '\n' ' ')" >>"$RAW/log.txt"
			prev="$d/attempt$k.json"
		done
	done
done
git -C "$REPO" log -1 --format='done at %h' >>"$RAW/log.txt"
