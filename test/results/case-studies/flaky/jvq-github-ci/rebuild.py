"""Rebuilds JUnit XML for each of jvq's own CI runs from api/runs.json.

GitHub keeps no test report for these runs, only each job's steps and the
annotations test.yml makes from `--- FAIL` lines. So the tests tracked are
the ones that failed somewhere in this history. For each run, job (OS) and
test step (Linux has two: plain and -race), a tracked test that exists at
that commit is:
  - failed, when the step failed and an annotation names it;
  - passed, when the step passed, or failed naming other tests;
  - left out, when the step didn't run, or failed naming no test (the run
    is listed in junit/left-out.txt with its reason).
The other tests passed in every run here and are left out. The race step
runs on Linux only. TestInstructionsSteadyWithGoroutines skips itself off
Linux (needValgrind in cmd/jvq/adapters_test.go), so it is skipped there.

    python3 rebuild.py      # writes junit/<history>/<NN>-<commit>.xml
"""
import json, os, re, shutil, subprocess
from xml.sax.saxutils import quoteattr

here = os.path.dirname(os.path.abspath(__file__))
root = subprocess.run(["git", "-C", here, "rev-parse", "--show-toplevel"],
                      capture_output=True, text=True, check=True).stdout.strip()
MODULE = "github.com/just-very-quickly/jvq"
runs = json.load(open(os.path.join(here, "api", "runs.json")))

STEPS = {"Run go test -count=1 ./...": "test", "Tests": "test",
         "Run go test -race -count=1 ./...": "race", "Tests with the race detector": "race"}
OS = {"test (ubuntu-latest)": "linux", "test (macos-latest)": "macos", "test (windows-latest)": "windows"}
FAIL = re.compile(r"--- FAIL: (\S+)")
LINUX_ONLY = {"TestInstructionsSteadyWithGoroutines"}


def step_of(title):
    # test.yml titles: "go test Linux" (plain step), "go test -race" (race step)
    if not title or not title.startswith("go test"):
        return None
    return "race" if "-race" in title else "test"


def failed_tests(job, step):
    out = []
    for a in job.get("annotations", []):
        if step_of(a["title"]) != step:
            continue
        m = FAIL.search(a["message"] or "")
        if m and m.group(1).split("/")[0] not in out:
            out.append(m.group(1).split("/")[0])
    return out


tracked = []
for r in runs:
    for j in r["jobs"]:
        for step in ("test", "race"):
            for t in failed_tests(j, step):
                if t not in tracked:
                    tracked.append(t)


def package_of(test, sha):
    p = subprocess.run(["git", "-C", root, "grep", "-l", "^func %s(" % test, sha, "--", "*_test.go"],
                       capture_output=True, text=True).stdout.split()
    if not p:
        return None
    d = os.path.dirname(p[0].split(":", 1)[1])
    return MODULE + ("/" + d if d else "")


out = os.path.join(here, "junit")
shutil.rmtree(out, ignore_errors=True)
os.makedirs(out)
left = []
for n, r in enumerate(runs, 1):
    sha = r["head_sha"]
    for j in r["jobs"]:
        osname = OS.get(j["name"])
        if not osname:
            continue
        for s in j["steps"]:
            step = STEPS.get(s["name"])
            if not step:
                continue
            hist = "%s-%s" % (osname, step)
            if s["conclusion"] not in ("success", "failure"):
                if osname == "linux":
                    left.append("%02d %s %s: step %s" % (n, sha[:7], hist, s["conclusion"]))
                continue
            failed = failed_tests(j, step) if s["conclusion"] == "failure" else []
            if s["conclusion"] == "failure" and not failed:
                left.append("%02d %s %s: failed, no test named in its annotations" % (n, sha[:7], hist))
                continue
            cases = []
            for t in tracked:
                pkg = package_of(t, sha)
                if pkg is None:
                    continue
                body = ""
                if t in LINUX_ONLY and osname != "linux":
                    body = "<skipped/>"
                if t in failed:
                    msgs = [a["message"] for a in j["annotations"] if step_of(a["title"]) == step]
                    body = "<failure message=%s></failure>" % quoteattr("; ".join(m.strip() for m in msgs))
                cases.append('  <testcase classname=%s name=%s>%s</testcase>' % (quoteattr(pkg), quoteattr(t), body))
            if not cases:
                continue
            d = os.path.join(out, hist)
            os.makedirs(d, exist_ok=True)
            with open(os.path.join(d, "%02d-%s.xml" % (n, sha)), "w") as f:
                f.write('<?xml version="1.0" encoding="UTF-8"?>\n<testsuite name="jvq CI run %d, %s">\n%s\n</testsuite>\n'
                        % (r["id"], j["name"], "\n".join(cases)))
with open(os.path.join(out, "left-out.txt"), "w") as f:
    f.write("\n".join(left) + "\n")
print("tracked:", ", ".join(tracked))
print("left out:", len(left))
