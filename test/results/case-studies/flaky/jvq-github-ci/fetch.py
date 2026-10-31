"""Saves jvq's own test workflow runs from the GitHub API into api/:
every run of test.yml, each run's jobs with their steps, and the
annotations of every job that failed (test.yml turns each `--- FAIL` line
into one). rebuild.py works only from these files.

    python3 fetch.py
"""
import json, os, subprocess

REPO = "just-very-quickly/jvq"
here = os.path.dirname(os.path.abspath(__file__))
api = os.path.join(here, "api")
os.makedirs(api, exist_ok=True)


def get(path):
    out = subprocess.run(["curl", "-sSf", "https://api.github.com/repos/" + REPO + path],
                         capture_output=True, text=True, check=True).stdout
    return json.loads(out)


runs = get("/actions/workflows/test.yml/runs?per_page=100")["workflow_runs"]
keep = []
for r in sorted(runs, key=lambda r: r["created_at"]):
    jobs = []
    for j in get("/actions/runs/%d/jobs" % r["id"])["jobs"]:
        job = {"id": j["id"], "name": j["name"], "conclusion": j["conclusion"],
               "steps": [{"name": s["name"], "conclusion": s["conclusion"]} for s in j["steps"]]}
        if j["conclusion"] != "success":
            job["annotations"] = [{"title": a.get("title"), "message": a.get("message")}
                                  for a in get("/check-runs/%d/annotations" % j["id"])]
        jobs.append(job)
    keep.append({"id": r["id"], "head_sha": r["head_sha"], "event": r["event"],
                 "branch": r["head_branch"], "run_attempt": r["run_attempt"],
                 "created_at": r["created_at"], "conclusion": r["conclusion"],
                 "title": r["display_title"], "jobs": jobs})
with open(os.path.join(api, "runs.json"), "w") as f:
    json.dump(keep, f, indent=1)
print(len(keep), "runs saved")
