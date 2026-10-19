// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/just-very-quickly/jvq/internal/fixture"
)

// The fixture's program does the same work as its benchmark, as a plain
// command: the time adapter times it whole, and with -json it reports its
// own batches in jvq's JSON format.

func TestTimeAdapterCatchesASlowdown(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: base})
	commit(t, r, fixture.Spec{Work: base * 3 / 2}, "50% slower")
	r.WriteConfig(fixture.ProgConfig("time", 5, 2000, ""))
	res := jvq(t, r.Dir, nil, "run")
	expect(t, res, 1)
	row := find(rows(t, res), "command", "time")
	if row == nil || row["verdict"] != "regression" {
		t.Fatalf("row %v\n%s", row, res.stdout)
	}
	if res.report["adapter"] != "time" {
		t.Errorf("adapter %v", res.report["adapter"])
	}
	assertNoWorktrees(t, r.Dir)
}

func TestTimeAdapterPassesNoChange(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: base})
	commit(t, r, fixture.Spec{Work: base, Extra: "comment"}, "no change")
	r.WriteConfig(fixture.ProgConfig("time", 5, 2000, "[budget]\ntime = \"25%\"\n"))
	res := jvq(t, r.Dir, nil, "run")
	expect(t, res, 0)
	if v := res.report["verdict"]; v == "regression" {
		t.Fatalf("verdict %v", v)
	}
}

func TestJSONAdapterCatchesASlowdown(t *testing.T) {
	r := newFixture(t, "sub", fixture.Spec{Work: base})
	commit(t, r, fixture.Spec{Work: base * 2}, "twice the work")
	r.WriteConfig(fixture.ProgConfig("json", 5, 200, ""))
	res := jvq(t, r.ModDir(), nil, "run")
	expect(t, res, 1)
	row := find(rows(t, res), "work", "time")
	if row == nil || row["verdict"] != "regression" {
		t.Fatalf("row %v\n%s", row, res.stdout)
	}
	// Five batches a run, reduced to their median: one value a round.
	if n, rounds := row["base"].(map[string]any)["n"].(float64), res.report["rounds"].(float64); n != rounds {
		t.Errorf("base n = %v, want one per round (%v)", n, rounds)
	}
}

func TestOutputFlagAndCheck(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: 2000})
	commit(t, r, fixture.Spec{Work: 2000, Extra: "a"}, "a")
	r.WriteConfig(fixture.ProgConfig("json", 3, 50, ""))
	// The command prints to stdout, but -output says to read a file it
	// never writes: an error that says so.
	res := jvq(t, r.Dir, nil, "run", "-output", "results.json")
	expect(t, res, 2)
	if !strings.Contains(res.stderr, "didn't write its output file") {
		t.Errorf("stderr:\n%s", res.stderr)
	}
	if res := jvq(t, r.Dir, nil, "run", "-adapter", "time", "-output", "x"); res.code != 2 || !strings.Contains(res.stderr, "time adapter") {
		t.Errorf("time with output: %d %s", res.code, res.stderr)
	}
	if res := jvq(t, r.Dir, nil, "run", "-adapter", "hyperfine"); res.code != 2 || !strings.Contains(res.stderr, "go, json, time") {
		t.Errorf("unknown adapter: %d %s", res.code, res.stderr)
	}
	os.WriteFile(filepath.Join(r.Dir, "jvq.toml"), []byte("adapter = \"json\"\noutput = \"out.json\"\n"), 0o644)
	res = jvq(t, r.Dir, nil, "check")
	if res.code != 0 || !strings.Contains(res.stdout, "output:      out.json") {
		t.Errorf("check:\n%s", res.stdout)
	}
}

func writeGo(t *testing.T, path string, values ...int) {
	t.Helper()
	var b strings.Builder
	b.WriteString("goos: linux\npkg: example.com/x\n")
	for _, v := range values {
		b.WriteString("BenchmarkA-8   1000   " + itoa(v) + " ns/op   16 B/op   1 allocs/op\n")
	}
	b.WriteString("PASS\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(v int) string { return strconv.Itoa(v) }

func TestCompareGoFiles(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, filepath.Join(dir, "old.txt"), 1000, 1010, 990, 1005, 995, 1002, 998, 1001)
	writeGo(t, filepath.Join(dir, "new.txt"), 1200, 1210, 1190, 1205, 1195, 1202, 1198, 1201)
	writeGo(t, filepath.Join(dir, "same.txt"), 1001, 1009, 991, 1004, 996, 1003, 997, 1000)
	report := filepath.Join(dir, "r.json")
	// Flags may come after the files.
	res := jvq(t, dir, nil, "compare", "old.txt", "new.txt", "-json", report)
	expect(t, res, 1)
	b, _ := os.ReadFile(report)
	if !strings.Contains(string(b), `"mode": "compare"`) || !strings.Contains(string(b), `"verdict": "regression"`) {
		t.Errorf("report:\n%s", b)
	}
	if !strings.Contains(res.stdout, "old old.txt vs new new.txt") || !strings.Contains(res.stdout, "REGRESSION") {
		t.Errorf("stdout:\n%s", res.stdout)
	}
	res = jvq(t, dir, nil, "compare", "old.txt", "same.txt")
	expect(t, res, 0)
	if !strings.Contains(res.stdout, "verdict: pass") {
		t.Errorf("stdout:\n%s", res.stdout)
	}
	// A config's budgets apply.
	os.WriteFile(filepath.Join(dir, "jvq.toml"), []byte("[budget]\ntime = \"30%\"\n"), 0o644)
	expect(t, jvq(t, dir, nil, "compare", "old.txt", "new.txt"), 0)
}

func TestCompareJSONFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"results": [{"name": "x", "unit": "ms", "values": [10, 10.2, 9.9, 10.1, 10]}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "b.json"), []byte("{\"name\": \"x\", \"unit\": \"ms\", \"values\": [8, 8.1, 7.9, 8, 8.05]}\n"), 0o644)
	res := jvq(t, dir, nil, "compare", "a.json", "b.json")
	expect(t, res, 0)
	if !strings.Contains(res.stdout, "improved") {
		t.Errorf("stdout:\n%s", res.stdout)
	}
	// The format can be named when the files have other endings.
	os.Rename(filepath.Join(dir, "a.json"), filepath.Join(dir, "a.out"))
	os.Rename(filepath.Join(dir, "b.json"), filepath.Join(dir, "b.out"))
	expect(t, jvq(t, dir, nil, "compare", "-adapter", "json", "a.out", "b.out"), 0)
}

func TestCompareErrors(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, filepath.Join(dir, "old.txt"), 1000, 1001, 1002)
	os.WriteFile(filepath.Join(dir, "empty.txt"), []byte("PASS\n"), 0o644)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"compare", "old.txt"}, "want two files"},
		{[]string{"compare", "old.txt", "a", "b"}, "want two files"},
		{[]string{"compare", "old.txt", "missing.txt"}, "missing.txt"},
		{[]string{"compare", "old.txt", "empty.txt"}, "no results"},
		{[]string{"compare", "-adapter", "time", "old.txt", "old.txt"}, "can't read saved files"},
		{[]string{"compare", "-adapter", "nope", "old.txt", "old.txt"}, "go, json, time"},
	}
	for _, c := range cases {
		res := jvq(t, dir, nil, c.args...)
		if res.code != 2 || !strings.Contains(res.stderr, c.want) {
			t.Errorf("%v: exit %d, stderr %q", c.args, res.code, res.stderr)
		}
	}
}

func TestCompareDashes(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, filepath.Join(dir, "-old.txt"), 1000, 1010, 990, 1005, 995)
	writeGo(t, filepath.Join(dir, "new.txt"), 1001, 1009, 991, 1004, 996)
	expect(t, jvq(t, dir, nil, "compare", "--", "-old.txt", "new.txt"), 0)
	expect(t, jvq(t, dir, nil, "compare", "-fail-on-inconclusive=false", "--", "-old.txt", "new.txt"), 0)
}

func TestNothingGatedSaysWhy(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"name": "x", "unit": "frames", "values": [10, 11, 12, 10]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "b.json"), []byte(`{"name": "x", "unit": "frames", "values": [90, 91, 92, 90]}`), 0o644)
	res := jvq(t, dir, nil, "compare", "a.json", "b.json")
	expect(t, res, 0)
	if !strings.Contains(res.stdout, "nothing was gated") || !strings.Contains(res.stdout, `"frames"`) {
		t.Errorf("stdout:\n%s", res.stdout)
	}
}

func TestSizeGate(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: 2000})
	commit(t, r, fixture.Spec{Work: 2000, Ballast: 400000}, "400 kB more data")
	exe := strings.TrimPrefix(fixture.ProgExe(), "./")
	r.WriteConfig(fixture.ProgConfig("time", 3, 10, "size = [\""+exe+"\"]\n[budget]\ntime = \"off\"\n"))
	res := jvq(t, r.Dir, nil, "run")
	expect(t, res, 1)
	row := find(rows(t, res), exe, "size")
	if row == nil || row["verdict"] != "regression" {
		t.Fatalf("size row %v\n%s", row, res.stdout)
	}
	if b := row["head"].(map[string]any)["median"].(float64) - row["base"].(map[string]any)["median"].(float64); b < 390000 || b > 450000 {
		t.Errorf("grew %v bytes, want about 400000", b)
	}
	// A pattern that matches nothing on either side is an error.
	r.WriteConfig(fixture.ProgConfig("time", 3, 10, "size = [\"nothing-*\"]\n"))
	if res := jvq(t, r.Dir, nil, "run"); res.code != 2 || !strings.Contains(res.stderr, "matches no file") {
		t.Errorf("no match: %d %s", res.code, res.stderr)
	}
	// A looser budget passes it.
	r.WriteConfig(fixture.ProgConfig("time", 3, 10, "size = [\""+exe+"\"]\n[budget]\ntime = \"off\"\nsize = \"50%\"\n"))
	expect(t, jvq(t, r.Dir, nil, "run"), 0)
}

func TestCompareJSONWithAGoConfig(t *testing.T) {
	// A jvq.toml that names a text adapter doesn't stop .json files being
	// read as json.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "jvq.toml"), []byte("adapter = \"go\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"name": "x", "unit": "ms", "values": [10, 10.2, 9.9, 10.1, 10]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "b.json"), []byte(`{"name": "x", "unit": "ms", "values": [10, 10.1, 9.9, 10.2, 10]}`), 0o644)
	expect(t, jvq(t, dir, nil, "compare", "a.json", "b.json"), 0)
}

func TestGitLabMergeRequestWithJUnit(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: base})
	first, _ := fixture.Git(r.Dir, "rev-parse", "HEAD")
	fixture.Git(r.Dir, "checkout", "-q", "-b", "feature")
	commit(t, r, fixture.Spec{Work: base * 2}, "twice the work")
	r.WriteConfig(fixture.Config(4, "10ms", ""))
	junit := filepath.Join(t.TempDir(), "jvq.xml")
	env := []string{"GITLAB_CI=true", "CI_MERGE_REQUEST_IID=3", "CI_MERGE_REQUEST_DIFF_BASE_SHA=" + first, "CI_MERGE_REQUEST_TARGET_BRANCH_NAME=main"}
	res := jvq(t, r.Dir, env, "run", "-junit", junit)
	expect(t, res, 1)
	if b := res.report["base"].(map[string]any); b["sha"] != first || b["ref"] != "CI_MERGE_REQUEST_DIFF_BASE_SHA" {
		t.Errorf("base %v", b)
	}
	x, err := os.ReadFile(junit)
	if err != nil || !strings.Contains(string(x), `failures="1"`) || !strings.Contains(string(x), `name="BenchmarkWork time"`) {
		t.Errorf("junit %v:\n%s", err, x)
	}
	// A base GitLab names that the clone lacks: the error says what to set.
	env[2] = "CI_MERGE_REQUEST_DIFF_BASE_SHA=0123456789abcdef0123456789abcdef01234567"
	res = jvq(t, r.Dir, env, "run")
	if res.code != 2 || !strings.Contains(res.stderr, "GIT_DEPTH") {
		t.Errorf("missing base: %d %s", res.code, res.stderr)
	}
}
