"""Copies record.sh's raw go test -json files into runs/, smaller: every
event is kept except the "output" lines of tests and packages that passed.
replay.sh checks that jvq judges the trimmed files the same as the raw.

    python3 trim.py RAW_DIR
"""
import json, os, sys

raw = sys.argv[1]
here = os.path.dirname(os.path.abspath(__file__))
for step in sorted(os.listdir(raw)):
    sd = os.path.join(raw, step)
    if not os.path.isdir(sd):
        continue
    for run in sorted(os.listdir(sd)):
        rd = os.path.join(sd, run)
        for name in sorted(os.listdir(rd)):
            if not name.endswith(".json"):
                continue
            lines = open(os.path.join(rd, name), encoding="utf-8", errors="replace").read().splitlines()
            events = []
            for line in lines:
                try:
                    events.append((line, json.loads(line)))
                except ValueError:
                    events.append((line, None))
            failed = set()
            for _, e in events:
                if isinstance(e, dict) and e.get("Action") == "fail":
                    failed.add((e.get("Package"), e.get("Test")))
            keep = []
            for line, e in events:
                if isinstance(e, dict) and e.get("Action") == "output":
                    t = e.get("Test")
                    if (e.get("Package"), t) not in failed and not (t and (e.get("Package"), t.split("/")[0]) in failed):
                        continue
                keep.append(line)
            od = os.path.join(here, "runs", step, run)
            os.makedirs(od, exist_ok=True)
            with open(os.path.join(od, name), "w", encoding="utf-8") as f:
                f.write("\n".join(keep) + "\n")
print("trimmed into", os.path.join(here, "runs"))
