#!/usr/bin/env python3
# Copyright JVQ.net 2026
# SPDX-License-Identifier: Apache-2.0
#
# The smoke check: runs a built jvq binary the way a user would, on a small
# git repository with a Go benchmark, and checks the answers.
#
#   python3 scripts/smoke.py <directory holding jvq or jvq.exe>
#
# It needs git and Go on PATH. The release workflow runs it on every binary
# it can run on the machine it was built for, before anything is published;
# scripts/record.sh runs it too.
#
# Checks:
#   - jvq version names the Version in cmd/jvq/version.go
#   - jvq check reads a jvq.toml
#   - a head that does 3x the work per op is a regression (exit 1, JSON says so)
#   - a head that changes only a comment is not (exit 0)
#   - a missing base is an error (exit 2)
import json
import os
import re
import subprocess
import sys
import tempfile

ROOT = os.path.realpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
WINDOWS = os.name == "nt"

BENCH = """package fixture

// {note}

import "testing"

const Work = {work}

var sink uint64

func BenchmarkWork(b *testing.B) {{
	for i := 0; i < b.N; i++ {{
		x := uint64(88172645463325252)
		for j := 0; j < Work; j++ {{
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
		}}
		sink = x
	}}
}}
"""


def fail(msg):
    print(f"smoke: FAIL: {msg}", file=sys.stderr, flush=True)
    sys.exit(1)


def ok(msg):
    print(f"smoke: ok: {msg}", flush=True)


def git(cwd, *args):
    env = dict(os.environ, GIT_AUTHOR_NAME="jvq", GIT_AUTHOR_EMAIL="jvq@example.com",
               GIT_COMMITTER_NAME="jvq", GIT_COMMITTER_EMAIL="jvq@example.com",
               GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1")
    r = subprocess.run(["git", *args], cwd=cwd, env=env, capture_output=True, text=True)
    if r.returncode:
        fail(f"git {' '.join(args)}: {r.stderr or r.stdout}")
    return r.stdout.strip()


def repo(path, head_work, head_note):
    os.makedirs(path)
    git(path, "init", "-q", "-b", "main")
    with open(os.path.join(path, "go.mod"), "w") as f:
        f.write("module example.com/smoke\n\ngo 1.21\n")
    with open(os.path.join(path, "work_test.go"), "w") as f:
        f.write(BENCH.format(work=20000, note="base"))
    git(path, "add", "-A")
    git(path, "commit", "-q", "-m", "base")
    with open(os.path.join(path, "work_test.go"), "w") as f:
        f.write(BENCH.format(work=head_work, note=head_note))
    git(path, "commit", "-q", "-am", "head")
    exe = "bench.test.exe" if WINDOWS else "./bench.test"
    out = "bench.test.exe" if WINDOWS else "bench.test"
    with open(os.path.join(path, "jvq.toml"), "w") as f:
        f.write(f'build = "go test -c -o {out} ."\n')
        f.write(f'command = "{exe} -test.run=NONE -test.bench=. -test.benchtime=20ms"\n')
        f.write("rounds = 5\nmax_rounds = 5\n")


def run(jvq, cwd, *args):
    env = {k: v for k, v in os.environ.items() if not k.startswith("GITHUB_")}
    r = subprocess.run([jvq, *args], cwd=cwd, env=env, capture_output=True, text=True, timeout=600)
    return r.returncode, r.stdout, r.stderr


def main():
    if len(sys.argv) != 2:
        fail("usage: smoke.py <directory holding jvq>")
    jvq = os.path.join(os.path.abspath(sys.argv[1]), "jvq.exe" if WINDOWS else "jvq")
    if not os.path.isfile(jvq):
        fail(f"no binary at {jvq}")
    with open(os.path.join(ROOT, "cmd", "jvq", "version.go"), encoding="utf-8") as f:
        want = re.search(r'^const Version = "(.*)"$', f.read(), re.M).group(1)

    code, out, err = run(jvq, ROOT, "version")
    if code != 0 or not out.startswith(f"jvq {want} "):
        fail(f"version: exit {code}, {out!r} {err!r}; want jvq {want}")
    ok(out.strip())

    with tempfile.TemporaryDirectory(prefix="jvq-smoke-") as tmp:
        slow = os.path.join(tmp, "slow")
        repo(slow, 60000, "three times the work")
        code, out, err = run(jvq, slow, "check")
        if code != 0 or "rounds:      5" not in out:
            fail(f"check: exit {code}\n{out}\n{err}")
        ok("check reads jvq.toml")

        report = os.path.join(tmp, "slow.json")
        code, out, err = run(jvq, slow, "run", "-json", report)
        if code != 1:
            fail(f"3x slower head: exit {code}, want 1\n{out}\n{err}")
        with open(report) as f:
            rep = json.load(f)
        if rep["verdict"] != "regression":
            fail(f"3x slower head: verdict {rep['verdict']}")
        row = next(r for r in rep["results"] if r["name"] == "BenchmarkWork" and r["metric"] == "time")
        ok(f"3x slower head is a regression (measured {row['change'] * 100:+.0f}%)")

        same = os.path.join(tmp, "same")
        repo(same, 20000, "a comment changed")
        code, out, err = run(jvq, same, "run")
        if code != 0:
            fail(f"unchanged head: exit {code}, want 0\n{out}\n{err}")
        ok("unchanged head passes")

        code, out, err = run(jvq, same, "run", "-base", "no-such-ref")
        if code != 2 or "no-such-ref" not in err:
            fail(f"missing base: exit {code}, want 2\n{err}")
        ok("missing base is an error")

        worktrees = git(same, "worktree", "list").splitlines()
        if len(worktrees) != 1:
            fail(f"worktrees left behind: {worktrees}")
        ok("no worktrees left behind")
    print("smoke: all checks passed", flush=True)


if __name__ == "__main__":
    main()
