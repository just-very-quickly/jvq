// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These run the jvq binary's flaky gate the way CI would: results files
// written by a test run, a history file kept between runs.

type ciRun struct {
	t    *testing.T
	dir  string
	hist string
}

func newCI(t *testing.T) *ciRun {
	dir := t.TempDir()
	return &ciRun{t: t, dir: dir, hist: filepath.Join(dir, "jvq-tests.jsonl")}
}

// goJSON writes go test -json events for tests with these attempts.
func goJSON(tests map[string]string) string {
	var b strings.Builder
	b.WriteString(`{"Action":"start","Package":"example.com/app"}` + "\n")
	for name, attempts := range tests {
		for _, a := range attempts {
			action := map[rune]string{'p': "pass", 'f': "fail", 's': "skip"}[a]
			fmt.Fprintf(&b, `{"Action":"run","Package":"example.com/app","Test":%q}`+"\n", name)
			fmt.Fprintf(&b, `{"Action":"output","Package":"example.com/app","Test":%q,"Output":"=== RUN %s\n"}`+"\n", name, name)
			fmt.Fprintf(&b, `{"Action":%q,"Package":"example.com/app","Test":%q,"Elapsed":0.01}`+"\n", action, name)
		}
	}
	end := "pass"
	for _, a := range tests {
		if strings.Contains(a, "f") {
			end = "fail"
		}
	}
	fmt.Fprintf(&b, `{"Action":%q,"Package":"example.com/app","Elapsed":0.1}`+"\n", end)
	return b.String()
}

// junit writes a JUnit file for tests with these attempts, one testcase
// element per attempt.
func junit(tests map[string]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<testsuites><testsuite name=\"web\">\n")
	for name, attempts := range tests {
		for _, a := range attempts {
			switch a {
			case 'p':
				fmt.Fprintf(&b, `<testcase classname="web.Checkout" name=%q time="0.1"/>`+"\n", name)
			case 'f':
				fmt.Fprintf(&b, `<testcase classname="web.Checkout" name=%q time="0.1"><failure message="timeout">stack</failure></testcase>`+"\n", name)
			case 's':
				fmt.Fprintf(&b, `<testcase classname="web.Checkout" name=%q><skipped/></testcase>`+"\n", name)
			}
		}
	}
	b.WriteString("</testsuite></testsuites>\n")
	return b.String()
}

// run writes the results and runs jvq flaky on them for commit, returning
// the result and the JSON report.
func (c *ciRun) run(commit string, goTests, junitTests map[string]string, extra ...string) (result, map[string]any) {
	c.t.Helper()
	var files []string
	if goTests != nil {
		p := filepath.Join(c.dir, "go.json")
		os.WriteFile(p, []byte(goJSON(goTests)), 0o644)
		files = append(files, p)
	}
	if junitTests != nil {
		p := filepath.Join(c.dir, "junit.xml")
		os.WriteFile(p, []byte(junit(junitTests)), 0o644)
		files = append(files, p)
	}
	jsonPath := filepath.Join(c.dir, "flaky.json")
	os.Remove(jsonPath)
	args := append([]string{"flaky", "-history", c.hist, "-json", jsonPath}, extra...)
	if commit != "" {
		args = append(args, "-commit", commit)
	}
	args = append(args, files...)
	res := jvq(c.t, c.dir, nil, args...)
	var rep map[string]any
	if b, err := os.ReadFile(jsonPath); err == nil {
		if err := json.Unmarshal(b, &rep); err != nil {
			c.t.Fatalf("bad JSON: %v\n%s", err, b)
		}
	}
	return res, rep
}

func status(t *testing.T, rep map[string]any, id string) map[string]any {
	t.Helper()
	res, _ := rep["result"].(map[string]any)
	rows, _ := res["tests_of_note"].([]any)
	for _, r := range rows {
		m := r.(map[string]any)
		if m["test"] == id {
			return m
		}
	}
	return nil
}

// The whole life of a flaky test in mixed Go and JUnit results: it blocks
// until it has flaked on two commits, then its failures are reported and
// don't block, while a real failure beside it still does; it leaves
// quarantine after 30 passing runs.
func TestFlakyGateLifecycle(t *testing.T) {
	c := newCI(t)
	const id = "web.Checkout.pays by card"
	res, rep := c.run("c1", map[string]string{"TestParse": "p"}, map[string]string{"pays by card": "f"})
	expect(t, res, 1)
	if s := status(t, rep, id); s["status"] != "failed" || s["blocks"] != true {
		t.Fatalf("first failure: %v", s)
	}
	// The job is re-run on the same commit and passes.
	res, _ = c.run("c1", map[string]string{"TestParse": "p"}, map[string]string{"pays by card": "p"})
	expect(t, res, 0)
	// On the next commit it fails once and passes on retry: into quarantine.
	res, rep = c.run("c2", map[string]string{"TestParse": "p"}, map[string]string{"pays by card": "fp"})
	expect(t, res, 0)
	if s := status(t, rep, id); s["quarantined"] != true || s["event"] != "entered" {
		t.Fatalf("want quarantine: %v", s)
	}
	if !strings.Contains(res.stdout, "into quarantine") {
		t.Errorf("stdout doesn't say so:\n%s", res.stdout)
	}
	// Its next failure doesn't block, and is still shown.
	res, rep = c.run("c3", map[string]string{"TestParse": "p"}, map[string]string{"pays by card": "f"})
	expect(t, res, 0)
	if s := status(t, rep, id); s["status"] != "quarantined" || s["blocks"] != false {
		t.Fatalf("quarantined failure: %v", s)
	}
	if !strings.Contains(res.stdout, "failed in quarantine, doesn't block") || !strings.Contains(res.stdout, "1 quarantined test(s) failed and didn't block") {
		t.Errorf("quarantined failure not visible:\n%s", res.stdout)
	}
	// A real failure in the Go tests still blocks.
	res, rep = c.run("c4", map[string]string{"TestParse": "f"}, map[string]string{"pays by card": "f"})
	expect(t, res, 1)
	if s := status(t, rep, "example.com/app.TestParse"); s["status"] != "failed" {
		t.Fatalf("TestParse: %v", s)
	}
	// Stable again: 30 passing runs take it out.
	for i := 0; i < 30; i++ {
		res, rep = c.run(fmt.Sprint("s", i), map[string]string{"TestParse": "p"}, map[string]string{"pays by card": "p"})
		expect(t, res, 0)
	}
	if s := status(t, rep, id); s["quarantined"] != false || s["event"] != "released" {
		t.Fatalf("want release after 30 passes: %v", s)
	}
	// Its next failure blocks again.
	res, _ = c.run("after", map[string]string{"TestParse": "p"}, map[string]string{"pays by card": "f"})
	expect(t, res, 1)
	b, _ := os.ReadFile(c.hist)
	if n := strings.Count(string(b), "\n"); n != 36 {
		t.Errorf("history has %d lines, want 36", n)
	}
}

// The main promise: quarantine never hides a test that really broke. With
// two retries it blocks in the run that broke it.
func TestFlakyGateBrokenTestBlocks(t *testing.T) {
	c := newCI(t)
	c.run("a", map[string]string{"TestNet": "fp"}, nil)
	c.run("b", map[string]string{"TestNet": "fp"}, nil)
	res, rep := c.run("broke", map[string]string{"TestNet": "fff"}, nil)
	expect(t, res, 1)
	if s := status(t, rep, "example.com/app.TestNet"); s["status"] != "broken" || s["quarantined"] != false {
		t.Fatalf("broken: %v", s)
	}
	if !strings.Contains(res.stdout, "broken (3 failed attempts in a row), left quarantine, blocks") {
		t.Errorf("stdout:\n%s", res.stdout)
	}
}

// A test that fails every attempt, on every re-run of the same commit,
// blocks every time and is never quarantined.
func TestFlakyGateConsistentFailure(t *testing.T) {
	c := newCI(t)
	for i := 0; i < 6; i++ {
		res, rep := c.run(fmt.Sprint("c", i/2), map[string]string{"TestDB": "ff", "TestOK": "p"}, nil)
		expect(t, res, 1)
		if s := status(t, rep, "example.com/app.TestDB"); s["quarantined"] != false || s["blocks"] != true {
			t.Fatalf("run %d: %v", i, s)
		}
	}
}

// A damaged history doesn't stop the gate: damaged lines are skipped with
// a note, the good runs still count, and the run is added.
func TestFlakyGateCorruptedHistory(t *testing.T) {
	c := newCI(t)
	c.run("a", map[string]string{"TestNet": "fp"}, nil)
	c.run("b", map[string]string{"TestNet": "fp"}, nil)
	b, _ := os.ReadFile(c.hist)
	lines := strings.SplitN(string(b), "\n", 2)
	damaged := lines[0] + "\n\x00\x00garbage from a broken cache\n" + lines[1][:len(lines[1])/2] + "\n" + lines[1]
	os.WriteFile(c.hist, []byte(damaged), 0o644)
	res, rep := c.run("c", map[string]string{"TestNet": "f"}, nil)
	expect(t, res, 0)
	if !strings.Contains(res.stdout, "skipped 2 damaged line(s)") {
		t.Errorf("no note:\n%s", res.stdout)
	}
	if s := status(t, rep, "example.com/app.TestNet"); s["status"] != "quarantined" {
		t.Fatalf("the good runs should still count: %v", s)
	}
	if rep["written"] != true {
		t.Error("the run wasn't added")
	}
	// A missing history starts a new one.
	os.Remove(c.hist)
	res, _ = c.run("d", map[string]string{"TestNet": "f"}, nil)
	expect(t, res, 1)
	if !strings.Contains(res.stdout, "doesn't exist yet, so this run starts the history") {
		t.Errorf("no note:\n%s", res.stdout)
	}
	if _, err := os.Stat(c.hist); err != nil {
		t.Error("history not created")
	}
}

func TestFlakyGateOptions(t *testing.T) {
	c := newCI(t)
	// -no-write judges without adding.
	res, rep := c.run("a", map[string]string{"TestA": "p"}, nil, "-no-write")
	expect(t, res, 0)
	if _, err := os.Stat(c.hist); err == nil || rep["written"] != false {
		t.Error("-no-write wrote the history")
	}
	// Markdown and the GitHub job summary, with annotations.
	c.run("a", map[string]string{"TestA": "fp"}, nil)
	c.run("b", map[string]string{"TestA": "fp"}, nil)
	summary := filepath.Join(c.dir, "summary.md")
	os.WriteFile(filepath.Join(c.dir, "go.json"), []byte(goJSON(map[string]string{"TestA": "f", "TestB": "f"})), 0o644)
	res = jvq(t, c.dir, []string{"GITHUB_ACTIONS=true", "GITHUB_STEP_SUMMARY=" + summary, "GITHUB_SHA=c3"}, "flaky", "-history", c.hist, "go.json")
	expect(t, res, 1)
	md, _ := os.ReadFile(summary)
	for _, want := range []string{"### jvq flaky", "**FAIL: 1 test(s) failed (example.com/app.TestB).", "| example.com/app.TestA | f | failed in quarantine, doesn't block"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("summary lacks %q:\n%s", want, md)
		}
	}
	if !strings.Contains(res.stderr, "::error title=jvq::example.com/app.TestB failed") || !strings.Contains(res.stderr, "::warning title=jvq::example.com/app.TestA failed; it's in quarantine") {
		t.Errorf("annotations:\n%s", res.stderr)
	}
	if !strings.Contains(res.stdout, "commit c3") {
		t.Errorf("GITHUB_SHA not used:\n%s", res.stdout)
	}
	// With no results it shows the quarantine.
	res = jvq(t, c.dir, nil, "flaky", "-history", c.hist)
	expect(t, res, 0)
	if !strings.Contains(res.stdout, "example.com/app.TestA") || !strings.Contains(res.stdout, "quarantined, ") {
		t.Errorf("state:\n%s", res.stdout)
	}
	// jvq.toml's [flaky] table sets the history.
	os.WriteFile(filepath.Join(c.dir, "jvq.toml"), []byte("[flaky]\nhistory = \"jvq-tests.jsonl\"\nrelease_after = 5\n"), 0o644)
	res = jvq(t, c.dir, nil, "flaky")
	expect(t, res, 0)
	if !strings.Contains(res.stdout, "0 of 5 passes to leave") {
		t.Errorf("config not used:\n%s", res.stdout)
	}
	res = jvq(t, c.dir, nil, "check")
	if !strings.Contains(res.stdout, "jvq-tests.jsonl (the newest 200 runs are kept)") || !strings.Contains(res.stdout, "out after 5 passing runs") {
		t.Errorf("check:\n%s", res.stdout)
	}
	// Without a history only in-run retries are judged, with a note.
	res, _ = c.run("z", map[string]string{"TestA": "fp"}, nil, "-history", "-")
	expect(t, res, 0)
	if !strings.Contains(res.stdout, "no test history") {
		t.Errorf("no note:\n%s", res.stdout)
	}
}

func TestFlakyGateErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	runHist := write("jvq-history.jsonl", `{"jvq":"0.6.0","date":"2026-10-19T00:00:00Z","base":{"sha":"a"},"head":{"sha":"b"},"verdict":"pass","results":[]}`+"\n")
	good := write("ok.json", goJSON(map[string]string{"TestA": "p"}))
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"flaky", filepath.Join(dir, "missing.xml")}, "missing.xml"},
		{[]string{"flaky", write("notes.txt", "hello")}, "not JUnit XML or go test -json"},
		{[]string{"flaky", write("empty.xml", "<testsuites></testsuites>")}, "no test results"},
		{[]string{"flaky", write("cut.xml", "<testsuite><testcase name='a'>")}, "JUnit XML"},
		{[]string{"flaky", "-history", runHist, good}, "benchmark history written by jvq run"},
		{[]string{"flaky"}, "give results files"},
	}
	for _, c := range cases {
		res := jvq(t, dir, nil, c.args...)
		if res.code != 2 || !strings.Contains(res.stderr, c.want) {
			t.Errorf("%v: exit %d, stderr %q; want 2 and %q", c.args, res.code, res.stderr, c.want)
		}
	}
	// The run history wasn't touched.
	if b, _ := os.ReadFile(runHist); strings.Count(string(b), "\n") != 1 {
		t.Error("run history changed")
	}
	// Flags may come after the files.
	res := jvq(t, dir, nil, "flaky", good, "-commit", "abc")
	expect(t, res, 0)
	// A history that can't be written doesn't change the verdict.
	res = jvq(t, dir, nil, "flaky", "-history", filepath.Join(dir, "no", "such", "h.jsonl"), good)
	expect(t, res, 0)
	if !strings.Contains(res.stderr, "couldn't add to the test history") {
		t.Errorf("stderr: %s", res.stderr)
	}
}

// In a git checkout, the commit is HEAD.
func TestFlakyGateCommitFromGit(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	sha, _ := cmd.Output()
	os.WriteFile(filepath.Join(dir, "r.json"), []byte(goJSON(map[string]string{"TestA": "p"})), 0o644)
	res := jvq(t, dir, nil, "flaky", "r.json")
	expect(t, res, 0)
	if !strings.Contains(res.stdout, "commit "+string(sha[:10])) {
		t.Errorf("want commit %s:\n%s", sha[:10], res.stdout)
	}
}

// Independent review, October 2026: once leak-check flakes have put a Go
// package's own entry in quarantine, a compile error in that package
// still blocks.
func TestFlakyGateBuildErrorBlocks(t *testing.T) {
	c := newCI(t)
	pkgFail := `{"Action":"start","Package":"example.com/app"}
{"Action":"output","Package":"example.com/app","Output":"goleak: found unexpected goroutines\n"}
{"Action":"fail","Package":"example.com/app","Elapsed":0.1}
`
	pkgPass := `{"Action":"start","Package":"example.com/app"}
{"Action":"run","Package":"example.com/app","Test":"TestA"}
{"Action":"pass","Package":"example.com/app","Test":"TestA"}
{"Action":"pass","Package":"example.com/app","Elapsed":0.1}
`
	buildFail := `{"ImportPath":"example.com/app [example.com/app.test]","Action":"build-fail"}
{"Action":"start","Package":"example.com/app"}
{"Action":"output","Package":"example.com/app","Output":"FAIL\texample.com/app [build failed]\n"}
{"Action":"fail","Package":"example.com/app","Elapsed":0,"FailedBuild":"example.com/app [example.com/app.test]"}
`
	p := filepath.Join(c.dir, "go.json")
	runOne := func(commit, src string) result {
		os.WriteFile(p, []byte(src), 0o644)
		return jvq(t, c.dir, nil, "flaky", "-history", c.hist, "-commit", commit, p)
	}
	for _, commit := range []string{"a", "b"} {
		runOne(commit, pkgFail)
		runOne(commit, pkgPass)
	}
	res := runOne("c", buildFail)
	expect(t, res, 1)
}

// Independent review, October 2026: a test name is printed so that it can
// never start a line with "::" (a GitHub Actions workflow command) or
// break a line, in the table, the verdict line or the Markdown summary.
func TestFlakyGateNamesCantInjectCommands(t *testing.T) {
	dir := t.TempDir()
	src := `<testsuites><testsuite name="">
<testcase name="::error::pwned"><failure/></testcase>
<testcase classname="c" name="x&#10;::warning::injected&#13;::add-mask::y"><failure/></testcase>
<testcase classname="c" name="a | b &lt;img src=x&gt;"><failure/></testcase>
</testsuite></testsuites>`
	p := filepath.Join(dir, "r.xml")
	os.WriteFile(p, []byte(src), 0o644)
	md := filepath.Join(dir, "summary.md")
	res := jvq(t, dir, []string{"GITHUB_ACTIONS=true", "GITHUB_STEP_SUMMARY=" + md}, "flaky", "-commit", "a", p)
	expect(t, res, 1)
	for _, out := range []string{res.stdout, res.stderr} {
		for _, l := range strings.FieldsFunc(out, func(r rune) bool { return r == '\n' || r == '\r' }) {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "::") && !strings.HasPrefix(l, "::error title=jvq::") && !strings.HasPrefix(l, "::warning title=jvq::") {
				t.Errorf("a test name started a workflow command: %q", l)
			}
		}
	}
	b, _ := os.ReadFile(md)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, "injected") && !strings.HasPrefix(l, "|") && !strings.HasPrefix(l, "**") {
			t.Errorf("a test name broke a Markdown line: %q", l)
		}
		if strings.Contains(l, "<img") {
			t.Errorf("raw HTML from a test name in the summary: %q", l)
		}
	}
}

// Independent review, October 2026: in a git checkout the commit is the
// checkout's HEAD even when GITHUB_SHA says otherwise. On
// pull_request_target and workflow_run, GITHUB_SHA is the base branch's
// latest commit, the same for every pull request, so a test broken by one
// pull request and passing in another looked flaky.
func TestFlakyGateCommitPrefersCheckout(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	sha, _ := cmd.Output()
	os.WriteFile(filepath.Join(dir, "r.json"), []byte(goJSON(map[string]string{"TestA": "p"})), 0o644)
	res := jvq(t, dir, []string{"GITHUB_SHA=0123456789abcdef0123456789abcdef01234567"}, "flaky", "r.json")
	expect(t, res, 0)
	if !strings.Contains(res.stdout, "commit "+string(sha[:10])) {
		t.Errorf("want the checkout's commit %s:\n%s", sha[:10], res.stdout)
	}
	// Outside a checkout, GITHUB_SHA is used.
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "r.json"), []byte(goJSON(map[string]string{"TestA": "p"})), 0o644)
	res = jvq(t, out, []string{"GITHUB_SHA=0123456789abcdef0123456789abcdef01234567"}, "flaky", "r.json")
	expect(t, res, 0)
	if !strings.Contains(res.stdout, "commit 0123456789") {
		t.Errorf("want GITHUB_SHA outside a checkout:\n%s", res.stdout)
	}
}

// Independent review, October 2026: a run in which no test ran (a -run
// filter that matched nothing leaves only packages that passed) is an
// error, like a run with no results, not a pass.
func TestFlakyGateNoTestRan(t *testing.T) {
	dir := t.TempDir()
	src := `{"Action":"start","Package":"example.com/app"}
{"Action":"output","Package":"example.com/app","Output":"testing: warning: no tests to run\n"}
{"Action":"pass","Package":"example.com/app","Elapsed":0.01}
`
	p := filepath.Join(dir, "r.json")
	os.WriteFile(p, []byte(src), 0o644)
	res := jvq(t, dir, nil, "flaky", "-commit", "a", p)
	if res.code != 2 || !strings.Contains(res.stderr, "no test") {
		t.Errorf("exit %d, stderr %q; want 2 and no test", res.code, res.stderr)
	}
	// A package that failed to build with no tests at all still fails.
	os.WriteFile(p, []byte(`{"Action":"fail","Package":"example.com/app","Elapsed":0,"FailedBuild":"example.com/app"}`+"\n"), 0o644)
	expect(t, jvq(t, dir, nil, "flaky", "-commit", "a", p), 1)
}

// A quarantined Go test that crashes stops its package, so the tests after
// it don't run. The gate passes (the crash is in quarantine) and reports
// those tests as not run, on real go test -json output from
// test/flakyrepo.
func TestFlakyGateCrashReportsNotRun(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	c := newCI(t)
	src, _ := filepath.Abs(filepath.Join("..", "..", "test", "flakyrepo"))
	goTest := func(mode, out string) {
		t.Helper()
		cmd := exec.Command("go", "test", "-json", "-count=2", "./...")
		cmd.Dir = src
		cmd.Env = append(os.Environ(), "JVQ_DEMO="+mode, "GOFLAGS=-mod=mod")
		b, _ := cmd.Output() // fails when a test fails; the JSON is what counts
		if !strings.Contains(string(b), `"Action"`) {
			t.Fatalf("go test -json printed no events:\n%s", b)
		}
		os.WriteFile(out, b, 0o644)
	}
	results := filepath.Join(c.dir, "go-test.json")
	jsonPath := filepath.Join(c.dir, "flaky.json")
	runGate := func(commit string) (result, map[string]any) {
		os.Remove(jsonPath)
		res := jvq(t, c.dir, nil, "flaky", "-history", c.hist, "-json", jsonPath, "-commit", commit, results)
		var rep map[string]any
		b, _ := os.ReadFile(jsonPath)
		json.Unmarshal(b, &rep)
		return res, rep
	}
	for _, commit := range []string{"c1", "c2"} {
		goTest("flaky", results)
		if res, _ := runGate(commit); res.code != 0 {
			t.Fatalf("flaky run on %s: exit %d\n%s%s", commit, res.code, res.stdout, res.stderr)
		}
	}
	goTest("crash", results)
	res, rep := runGate("c3")
	if res.code != 0 {
		t.Fatalf("crash in quarantine blocked: exit %d\n%s%s", res.code, res.stdout, res.stderr)
	}
	if s := status(t, rep, "example.com/flakydemo.TestB"); s == nil || s["status"] != "not_run" {
		t.Fatalf("TestB not reported as not run: %v\n%s", s, res.stdout)
	}
	if !strings.Contains(res.stdout, "didn't run because their package stopped") {
		t.Errorf("no not-run line in:\n%s", res.stdout)
	}
	// The package passing again: nothing is not run.
	goTest("", results)
	if res, rep := runGate("c4"); res.code != 0 || status(t, rep, "example.com/flakydemo.TestB") != nil {
		t.Fatalf("after recovery: exit %d %v", res.code, status(t, rep, "example.com/flakydemo.TestB"))
	}
}
