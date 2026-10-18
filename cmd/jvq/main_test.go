// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/just-very-quickly/jvq/internal/fixture"
)

// These tests build the jvq binary and run it on fixture repositories, the
// way a user or CI would: real git, real worktrees, real go test.

var jvqBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "jvq-bin-")
	if err != nil {
		panic(err)
	}
	name := "jvq"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	jvqBin = filepath.Join(dir, name)
	out, err := exec.Command("go", "build", "-o", jvqBin, ".").CombinedOutput()
	if err != nil {
		panic("building jvq: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type result struct {
	code           int
	stdout, stderr string
	report         map[string]any
}

func jvq(t *testing.T, dir string, env []string, args ...string) result {
	t.Helper()
	jsonPath := filepath.Join(t.TempDir(), "report.json")
	if len(args) > 0 && args[0] == "run" {
		args = append([]string{"run", "-json", jsonPath}, args[1:]...)
	}
	cmd := exec.Command(jvqBin, args...)
	cmd.Dir = dir
	cmd.Env = append(cleanEnv(), env...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	res := result{stdout: so.String(), stderr: se.String()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(jsonPath); err == nil {
		if err := json.Unmarshal(b, &res.report); err != nil {
			t.Fatalf("bad JSON report: %v\n%s", err, b)
		}
	}
	return res
}

// cleanEnv drops CI variables that would change how jvq picks the base.
func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GITHUB_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func newFixture(t *testing.T, subdir string, base fixture.Spec) *fixture.Repo {
	t.Helper()
	r, err := fixture.New(t.TempDir(), subdir, base)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func commit(t *testing.T, r *fixture.Repo, s fixture.Spec, msg string) string {
	t.Helper()
	sha, err := r.Commit(s, msg)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func rows(t *testing.T, res result) []map[string]any {
	t.Helper()
	raw, _ := res.report["results"].([]any)
	var out []map[string]any
	for _, x := range raw {
		out = append(out, x.(map[string]any))
	}
	return out
}

func find(rs []map[string]any, name, metric string) map[string]any {
	for _, r := range rs {
		if r["name"] == name && r["metric"] == metric {
			return r
		}
	}
	return nil
}

func expect(t *testing.T, res result, code int) {
	t.Helper()
	if res.code != code {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", res.code, code, res.stdout, res.stderr)
	}
}

const base = 20000

func TestCatchesASlowdown(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: base})
	headSHA := commit(t, r, fixture.Spec{Work: base * 3 / 2}, "50% slower")
	if err := r.WriteConfig(fixture.Config(5, "20ms", "")); err != nil {
		t.Fatal(err)
	}
	res := jvq(t, r.Dir, nil, "run")
	expect(t, res, 1)
	if res.report["verdict"] != "regression" {
		t.Fatalf("verdict %v", res.report["verdict"])
	}
	row := find(rows(t, res), "BenchmarkWork", "time")
	if row == nil || row["verdict"] != "regression" {
		t.Fatalf("row %v", row)
	}
	// The work per op grew 50%; on a noisy machine the measured change
	// wanders around that, so only check it's in the right region.
	if c := row["change"].(float64); c < 0.2 || c > 1.2 {
		t.Errorf("change %v, want about +50%%", c)
	}
	head := res.report["head"].(map[string]any)
	if head["sha"] != headSHA {
		t.Errorf("head %v, want %s", head, headSHA)
	}
	if base := res.report["base"].(map[string]any); base["ref"] != "head^" {
		t.Errorf("on main, base should be head's parent: %v", base)
	}
	if !strings.Contains(res.stdout, "REGRESSION") || !strings.Contains(res.stderr, "round 5/5") {
		t.Errorf("output:\n%s\n%s", res.stdout, res.stderr)
	}
	// Order is ABBA, for at least the five rounds asked for.
	order := res.report["order"].([]any)
	if len(order) < 10 || len(order)%2 != 0 || order[0] != "base" || order[1] != "head" || order[2] != "head" || order[3] != "base" {
		t.Errorf("order %v", order)
	}
	assertNoWorktrees(t, r.Dir)
}

func TestPassesWhenNothingChanged(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: base})
	commit(t, r, fixture.Spec{Work: base, Extra: "a comment changed"}, "no change")
	r.WriteConfig(fixture.Config(5, "20ms", ""))
	res := jvq(t, r.Dir, nil, "run")
	expect(t, res, 0)
	if v := res.report["verdict"]; v == "regression" {
		t.Fatalf("verdict %v", v)
	}
	assertNoWorktrees(t, r.Dir)
}

func TestAllocationGate(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: 2000})
	commit(t, r, fixture.Spec{Work: 2000, Allocs: 1}, "one more allocation")
	r.WriteConfig(fixture.Config(3, "10ms", "[budget]\ntime = \"off\"\nallocs = \"0\"\n"))
	res := jvq(t, r.Dir, nil, "run")
	expect(t, res, 1)
	row := find(rows(t, res), "BenchmarkWork", "allocs")
	if row == nil || row["verdict"] != "regression" {
		t.Fatalf("allocs row %v", row)
	}
	if find(rows(t, res), "BenchmarkWork", "time")["verdict"] != "info" {
		t.Errorf("time budget off should report info")
	}
}

func TestRenamedBenchmarkIsNewAndRemoved(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: 2000})
	commit(t, r, fixture.Spec{Work: 2000, Name: "BenchmarkRenamed"}, "rename")
	r.WriteConfig(fixture.Config(3, "10ms", ""))
	res := jvq(t, r.Dir, nil, "run")
	expect(t, res, 0) // nothing gated: inconclusive, which passes by default
	if res.report["verdict"] != "inconclusive" {
		t.Errorf("verdict %v", res.report["verdict"])
	}
	if find(rows(t, res), "BenchmarkWork", "time")["verdict"] != "removed" ||
		find(rows(t, res), "BenchmarkRenamed", "time")["verdict"] != "new" {
		t.Errorf("rows %v", rows(t, res))
	}
	r.WriteConfig(fixture.Config(3, "10ms", "fail_on_inconclusive = true\n"))
	expect(t, jvq(t, r.Dir, nil, "run"), 1)
}

func TestBaseThatDoesNotBuild(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: 2000, Broken: true})
	commit(t, r, fixture.Spec{Work: 2000}, "fixed")
	r.WriteConfig(fixture.Config(3, "10ms", ""))
	res := jvq(t, r.Dir, nil, "run")
	expect(t, res, 2)
	if !strings.Contains(res.stderr, "base build failed") || !strings.Contains(res.stderr, "not an int") {
		t.Errorf("stderr:\n%s", res.stderr)
	}
	res = jvq(t, r.Dir, nil, "run", "-base-may-fail")
	expect(t, res, 0)
	if !strings.Contains(res.stdout, "nothing compared") {
		t.Errorf("stdout:\n%s", res.stdout)
	}
	assertNoWorktrees(t, r.Dir)
}

func TestHeadThatDoesNotBuild(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: 2000})
	commit(t, r, fixture.Spec{Work: 2000, Broken: true}, "broken")
	r.WriteConfig(fixture.Config(3, "10ms", ""))
	res := jvq(t, r.Dir, nil, "run", "-base-may-fail")
	expect(t, res, 2) // --base-may-fail doesn't excuse head
	if !strings.Contains(res.stderr, "head build failed") {
		t.Errorf("stderr:\n%s", res.stderr)
	}
}

func TestShallowCloneSaysHowToFix(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: 2000})
	commit(t, r, fixture.Spec{Work: 2001}, "two")
	clone := filepath.Join(t.TempDir(), "clone")
	if _, err := fixture.Git(filepath.Dir(clone), "clone", "-q", "--depth", "1", fixture.FileURL(r.Dir), clone); err != nil {
		t.Fatal(err)
	}
	res := jvq(t, clone, nil, "run", "--", fixture.Command("10ms"))
	expect(t, res, 2)
	if !strings.Contains(res.stderr, "fetch-depth: 0") {
		t.Errorf("stderr:\n%s", res.stderr)
	}
}

func TestExplicitBaseAndSubdirectory(t *testing.T) {
	r := newFixture(t, "bench/go", fixture.Spec{Work: base})
	first, _ := fixture.Git(r.Dir, "rev-parse", "HEAD")
	commit(t, r, fixture.Spec{Work: base, Extra: "noise"}, "unrelated")
	commit(t, r, fixture.Spec{Work: base * 2}, "twice as slow")
	r.WriteConfig(fixture.Config(4, "10ms", ""))
	res := jvq(t, r.ModDir(), nil, "run", "-base", first, "-q")
	expect(t, res, 1)
	if b := res.report["base"].(map[string]any); b["sha"] != first {
		t.Errorf("base %v, want %s", b, first)
	}
	if res.stderr != "" && strings.Contains(res.stderr, "round") {
		t.Errorf("-q still printed progress:\n%s", res.stderr)
	}
}

func TestCommandAfterDashes(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: 2000})
	commit(t, r, fixture.Spec{Work: 2000, Extra: "x"}, "same")
	args := append([]string{"run", "-rounds", "3", "-build", fixture.Build(), "--"}, strings.Fields(fixture.Command("10ms"))...)
	res := jvq(t, r.Dir, nil, args...)
	expect(t, res, 0)
	if !strings.Contains(res.report["command"].(string), "-test.benchtime=10ms") {
		t.Errorf("command %v", res.report["command"])
	}
}

func TestNoResultsExplainsTheAdapter(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: 2000})
	commit(t, r, fixture.Spec{Work: 2001}, "two")
	res := jvq(t, r.Dir, nil, "run", "-rounds", "3", "--", "echo nothing here")
	expect(t, res, 2)
	if !strings.Contains(res.stderr, "no benchmark results") || !strings.Contains(res.stderr, "Benchmark lines") {
		t.Errorf("stderr:\n%s", res.stderr)
	}
}

func TestGitHubStepSummaryAndAnnotations(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: base})
	commit(t, r, fixture.Spec{Work: base * 2}, "slower")
	r.WriteConfig(fixture.Config(4, "10ms", ""))
	summary := filepath.Join(t.TempDir(), "summary.md")
	os.WriteFile(summary, []byte("earlier step\n"), 0o644)
	res := jvq(t, r.Dir, []string{"GITHUB_ACTIONS=true", "GITHUB_STEP_SUMMARY=" + summary}, "run")
	expect(t, res, 1)
	b, _ := os.ReadFile(summary)
	if !strings.HasPrefix(string(b), "earlier step\n") || !strings.Contains(string(b), "### jvq: regression") {
		t.Errorf("summary:\n%s", b)
	}
	if !strings.Contains(res.stderr, "::error title=jvq::BenchmarkWork time") {
		t.Errorf("no annotation:\n%s", res.stderr)
	}
}

func TestGitHubBaseRef(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: 2000})
	first, _ := fixture.Git(r.Dir, "rev-parse", "HEAD")
	fixture.Git(r.Dir, "branch", "release")
	commit(t, r, fixture.Spec{Work: 2000, Extra: "a"}, "a")
	commit(t, r, fixture.Spec{Work: 2000, Extra: "b"}, "b")
	r.WriteConfig(fixture.Config(3, "10ms", ""))
	res := jvq(t, r.Dir, []string{"GITHUB_BASE_REF=release"}, "run")
	expect(t, res, 0)
	if b := res.report["base"].(map[string]any); b["sha"] != first {
		t.Errorf("base %v, want the release branch %s", b, first)
	}
}

func TestErrors(t *testing.T) {
	r := newFixture(t, "", fixture.Spec{Work: 2000})
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"run", "-base", "HEAD"}, "same commit"},
		{[]string{"run", "-base", "nope"}, `can't find "nope"`},
		{[]string{"run", "-rounds", "2"}, "at least 3"},
		{[]string{"run", "-timeout", "soon"}, "--timeout"},
		{[]string{"run", "-config", "missing.toml"}, "doesn't exist"},
		{[]string{"frobnicate"}, "unknown command"},
	}
	for _, c := range cases {
		res := jvq(t, r.Dir, nil, c.args...)
		if res.code != 2 || !strings.Contains(res.stderr, c.want) {
			t.Errorf("%v: exit %d, stderr %q", c.args, res.code, res.stderr)
		}
	}
	// No command and no go.mod: say so.
	plain := t.TempDir()
	fixture.Git(plain, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(plain, "a"), []byte("1"), 0o644)
	fixture.Git(plain, "add", "a")
	fixture.Git(plain, "commit", "-q", "-m", "1")
	os.WriteFile(filepath.Join(plain, "a"), []byte("2"), 0o644)
	fixture.Git(plain, "commit", "-q", "-am", "2")
	if res := jvq(t, plain, nil, "run"); res.code != 2 || !strings.Contains(res.stderr, "no benchmark command") {
		t.Errorf("no command: %d %s", res.code, res.stderr)
	}
	if res := jvq(t, t.TempDir(), nil, "run", "--", "x"); res.code != 2 || !strings.Contains(res.stderr, "git repository") {
		t.Errorf("not a repo: %d %s", res.code, res.stderr)
	}
}

func TestVersionHelpCheck(t *testing.T) {
	dir := t.TempDir()
	res := jvq(t, dir, nil, "version")
	expect(t, res, 0)
	if !strings.HasPrefix(res.stdout, "jvq "+Version+" ") {
		t.Errorf("version %q", res.stdout)
	}
	if res := jvq(t, dir, nil, "help"); res.code != 0 || !strings.Contains(res.stdout, "jvq run") {
		t.Errorf("help: %+v", res)
	}
	if res := jvq(t, dir, nil); res.code != 2 {
		t.Errorf("no arguments: exit %d", res.code)
	}
	res = jvq(t, dir, nil, "check")
	expect(t, res, 0)
	if !strings.Contains(res.stdout, "defaults") || !strings.Contains(res.stdout, "time 5%, bytes off, allocs off") {
		t.Errorf("check:\n%s", res.stdout)
	}
	os.WriteFile(filepath.Join(dir, "jvq.toml"), []byte("rounds = 7\n[budget.bench.\"BenchmarkX*\"]\ntime = \"2%\"\nallocs = \"0\"\n[ignore]\nbench = [\"B*\"]\n"), 0o644)
	res = jvq(t, dir, nil, "check")
	expect(t, res, 0)
	for _, want := range []string{"rounds:      7, up to 30", "BenchmarkX*: time 2%, allocs 0", "ignored:     B*"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("check lacks %q:\n%s", want, res.stdout)
		}
	}
	os.WriteFile(filepath.Join(dir, "jvq.toml"), []byte("rounds = 1\n"), 0o644)
	if res := jvq(t, dir, nil, "check"); res.code != 2 || !strings.Contains(res.stderr, "rounds = 1") {
		t.Errorf("bad config: %+v", res)
	}
}

func TestShellJoin(t *testing.T) {
	if got := shellJoin([]string{"go test -bench=."}); got != "go test -bench=." {
		t.Errorf("single word %q", got)
	}
	got := shellJoin([]string{"go", "test", "-bench=Foo Bar", "it's"})
	want := `go test '-bench=Foo Bar' 'it'\''s'`
	if runtime.GOOS == "windows" {
		want = `go test "-bench=Foo Bar" "it's"`
	}
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if quoteArg("") == "" {
		t.Error("empty argument vanished")
	}
}

func assertNoWorktrees(t *testing.T, dir string) {
	t.Helper()
	out, err := fixture.Git(dir, "worktree", "list")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Split(strings.TrimSpace(out), "\n")); n != 1 {
		t.Errorf("worktrees left behind:\n%s", out)
	}
}

func jvqRunDirs(t *testing.T) map[string]bool {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(os.TempDir(), "jvq-run-*"))
	out := map[string]bool{}
	for _, d := range m {
		out[d] = true
	}
	return out
}

// startSlow starts jvq on a benchmark slow enough to interrupt, and waits
// until its worktrees exist.
func startSlow(t *testing.T) (*exec.Cmd, *fixture.Repo, string) {
	t.Helper()
	r := newFixture(t, "", fixture.Spec{Work: base})
	commit(t, r, fixture.Spec{Work: base, Extra: "x"}, "same")
	r.WriteConfig(fixture.Config(50, "2s", ""))
	before := jvqRunDirs(t)
	cmd := exec.Command(jvqBin, "run")
	cmd.Dir = r.Dir
	cmd.Env = cleanEnv()
	se := &syncBuffer{}
	cmd.Stderr = se
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(se.String(), "warm-up") {
			for d := range jvqRunDirs(t) {
				if !before[d] {
					return cmd, r, d
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	cmd.Process.Kill()
	t.Fatalf("jvq never got to its warm-up run:\n%s", se.String())
	return nil, nil, ""
}

// syncBuffer is read by the test while exec writes the child's output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestInterruptCleansUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sending an interrupt to another process is a console event on Windows")
	}
	cmd, r, ws := startSlow(t)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 {
			t.Errorf("exit %v, want 2", err)
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		t.Fatal("jvq didn't stop within 30s of an interrupt")
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Errorf("workspace %s left behind", ws)
	}
	assertNoWorktrees(t, r.Dir)
}

func TestKilledRunIsCleanedUpByTheNext(t *testing.T) {
	cmd, r, ws := startSlow(t)
	if err := cmd.Process.Kill(); err != nil { // SIGKILL: no chance to clean up
		t.Fatal(err)
	}
	cmd.Wait()
	// The benchmark the killed jvq started may still be finishing a round;
	// on Windows its files can't be removed until it has.
	wait := 500 * time.Millisecond
	if runtime.GOOS == "windows" {
		wait = 8 * time.Second
	}
	time.Sleep(wait)
	if _, err := os.Stat(ws); err != nil {
		t.Fatalf("a killed run should leave its workspace (that's the point of the test): %v", err)
	}
	if out, _ := fixture.Git(r.Dir, "worktree", "list"); len(strings.Split(out, "\n")) != 3 {
		t.Fatalf("expected the killed run's two worktrees:\n%s", out)
	}
	// The next run, even a quick one, removes it.
	r.WriteConfig(fixture.Config(3, "10ms", ""))
	res := jvq(t, r.Dir, nil, "run")
	expect(t, res, 0)
	if !strings.Contains(res.stderr, "left by an earlier run") {
		t.Errorf("stderr:\n%s", res.stderr)
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Errorf("stale workspace %s still there", ws)
	}
	assertNoWorktrees(t, r.Dir)
}
