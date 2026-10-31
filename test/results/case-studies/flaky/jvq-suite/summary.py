"""Summarises output/json: per history, each run that wasn't a plain pass."""
import glob, json, os
here = os.path.dirname(os.path.abspath(__file__))
rows = {}
for p in sorted(glob.glob(os.path.join(here, "output", "json", "*.json"))):
    name = os.path.basename(p)[:-5]
    hist, run, sha = name.rsplit("-", 2)[0], name.rsplit("-", 2)[1], name.rsplit("-", 2)[2]
    r = json.load(open(p))["result"]
    rows.setdefault(hist, []).append((run, sha, r))
print("| History | Runs | Exit 1 | Ever in quarantine |")
print("|---|---:|---:|---|")
for h, rs in rows.items():
    fails = sum(1 for _, _, r in rs if r["verdict"] == "fail")
    q = sorted({t["test"].rsplit(".", 1)[-1] for _, _, r in rs for t in (r.get("tests_of_note") or []) if t.get("quarantined")})
    print("| %s | %d | %d | %s |" % (h, len(rs), fails, ", ".join(q) or "none"))
print()
for h, rs in rows.items():
    for run, sha, r in rs:
        notes = (r.get("tests_of_note") or [])
        if r["verdict"] == "pass" and not notes:
            continue
        for t in notes:
            print("- %s run %s (%s): %s, attempts %s, status %s, blocks %s, flaky commits %d, last runs %s"
                  % (h, run, sha[:7], t["test"].rsplit(".", 1)[-1], t["attempts"], t["status"],
                     t["blocks"], t["flaky_commits"], t["recent"]))
