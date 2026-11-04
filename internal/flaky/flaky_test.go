// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package flaky

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// sim is a CI history being built run by run.
type sim struct {
	t       *testing.T
	s       Settings
	entries []Entry
}

func newSim(t *testing.T) *sim { return &sim{t: t, s: DefaultSettings()} }

// run judges one CI run on commit with these attempts, adds it to the
// history, and returns the result.
func (m *sim) run(commit string, tests map[string]string) Result {
	e := NewEntry("test", time.Date(2026, 10, 31, 0, 0, len(m.entries), 0, time.UTC), commit, tests)
	r := Judge(m.entries, &e, m.s)
	m.entries = append(m.entries, e)
	return r
}

func row(r Result, id string) (Row, bool) {
	for _, x := range r.Rows {
		if x.ID == id {
			return x, true
		}
	}
	return Row{}, false
}

func wantStatus(t *testing.T, r Result, id, status string, blocks bool) {
	t.Helper()
	x, ok := row(r, id)
	if !ok {
		t.Fatalf("%s isn't listed; rows %+v", id, r.Rows)
	}
	if x.Status != status || x.Blocks != blocks {
		t.Fatalf("%s: status %s blocks %v, want %s %v (%+v)", id, x.Status, x.Blocks, status, blocks, x)
	}
}

// A test that passes and fails on the same commit, twice, goes into
// quarantine and its next failure doesn't block; until then each plain
// failure blocks.
func TestFlakyStopsBlocking(t *testing.T) {
	m := newSim(t)
	r := m.run("a", map[string]string{"T": "f", "U": "p"})
	wantStatus(t, r, "T", StatusFailed, true)
	if r.Verdict != VerdictFail {
		t.Fatal("a first failure blocks")
	}
	// The job is re-run on the same commit and passes: one flaky commit.
	r = m.run("a", map[string]string{"T": "p", "U": "p"})
	if x, _ := row(r, "T"); r.Verdict != VerdictPass || x.Quarantined {
		t.Fatalf("one flaky commit isn't enough: %+v", r)
	}
	// On commit b it fails and passes on retry: flaky, not blocking, and
	// the second flaky commit puts it in quarantine.
	r = m.run("b", map[string]string{"T": "fp", "U": "p"})
	wantStatus(t, r, "T", StatusFlaky, false)
	if x, _ := row(r, "T"); !x.Quarantined || x.Event != EventEntered || x.FlakyCommits != 2 {
		t.Fatalf("want quarantine after two flaky commits: %+v", x)
	}
	// Now a failure on a new commit is reported and doesn't block.
	r = m.run("c", map[string]string{"T": "f", "U": "p"})
	wantStatus(t, r, "T", StatusQuarantined, false)
	if r.Verdict != VerdictPass || r.Quarantined != 1 || r.InQuarantine != 1 {
		t.Fatalf("result %+v", r)
	}
	// A real failure of another test still blocks beside it.
	r = m.run("d", map[string]string{"T": "f", "U": "f"})
	wantStatus(t, r, "U", StatusFailed, true)
	wantStatus(t, r, "T", StatusQuarantined, false)
	if r.Verdict != VerdictFail {
		t.Fatal("U's failure must block")
	}
}

func quarantined(t *testing.T, m *sim, id string) {
	t.Helper()
	m.run("q1", map[string]string{id: "fp"})
	r := m.run("q2", map[string]string{id: "fp"})
	if x, _ := row(r, id); !x.Quarantined {
		t.Fatalf("setup: %s not in quarantine: %+v", id, x)
	}
}

// The main promise: a quarantined test that really breaks blocks. With
// retries it blocks in the run that broke it.
func TestBrokenBlocksWithRetries(t *testing.T) {
	m := newSim(t)
	quarantined(t, m, "T")
	r := m.run("broke", map[string]string{"T": "fff"})
	wantStatus(t, r, "T", StatusBroken, true)
	if x, _ := row(r, "T"); x.Quarantined || x.Event != EventBroken {
		t.Fatalf("a broken test leaves quarantine: %+v", x)
	}
	// And it keeps blocking after: it's out of quarantine, with no new
	// flaky commits.
	r = m.run("next", map[string]string{"T": "fff"})
	wantStatus(t, r, "T", StatusFailed, true)
	r = m.run("next2", map[string]string{"T": "f"})
	wantStatus(t, r, "T", StatusFailed, true)
}

// Without retries a quarantined test that broke is listed as a
// quarantined failure until its third failure in a row, which blocks.
func TestBrokenBlocksWithoutRetries(t *testing.T) {
	m := newSim(t)
	quarantined(t, m, "T")
	wantStatus(t, m.run("c1", map[string]string{"T": "f"}), "T", StatusQuarantined, false)
	wantStatus(t, m.run("c2", map[string]string{"T": "f"}), "T", StatusQuarantined, false)
	wantStatus(t, m.run("c3", map[string]string{"T": "f"}), "T", StatusBroken, true)
	wantStatus(t, m.run("c4", map[string]string{"T": "f"}), "T", StatusFailed, true)
}

// A test that fails every time, re-runs of the same commit included,
// never goes into quarantine, however often it runs.
func TestConsistentFailureNeverQuarantined(t *testing.T) {
	m := newSim(t)
	for i := 0; i < 50; i++ {
		c := fmt.Sprint("c", i/2) // every commit twice, as a re-run
		r := m.run(c, map[string]string{"T": "ff", "U": "p"})
		x, _ := row(r, "T")
		if x.Quarantined || !x.Blocks {
			t.Fatalf("run %d: %+v", i, x)
		}
	}
	// Flaky in the past, then failing every attempt: it doesn't enter.
	m = newSim(t)
	m.run("a", map[string]string{"T": "fp"})
	r := m.run("b", map[string]string{"T": "ffp"})
	if x, _ := row(r, "T"); !x.Quarantined {
		t.Fatalf("setup: %+v", x)
	}
	m = newSim(t)
	m.run("a", map[string]string{"T": "fp"})
	m.run("b", map[string]string{"T": "f"})
	r = m.run("b", map[string]string{"T": "ff"}) // second flaky commit, but 3 failures in a row
	wantStatus(t, r, "T", StatusFailed, true)
	if x, _ := row(r, "T"); x.Quarantined {
		t.Fatalf("a test failing three attempts in a row mustn't enter quarantine: %+v", x)
	}
}

// Quarantine is reversible: 30 passing runs in a row take a test out, and
// its next failure blocks again.
func TestStableLeavesQuarantine(t *testing.T) {
	m := newSim(t)
	quarantined(t, m, "T")
	var r Result
	for i := 0; i < DefaultReleaseAfter; i++ {
		r = m.run(fmt.Sprint("s", i), map[string]string{"T": "p"})
		x, _ := row(r, "T")
		if i < DefaultReleaseAfter-1 && !x.Quarantined {
			t.Fatalf("left after %d passes", i+1)
		}
	}
	x, _ := row(r, "T")
	if x.Quarantined || x.Event != EventReleased {
		t.Fatalf("want released after %d passes: %+v", DefaultReleaseAfter, x)
	}
	r = m.run("later", map[string]string{"T": "f"})
	wantStatus(t, r, "T", StatusFailed, true)
	// It needs two new flaky commits to go back in: the old evidence was
	// cleared.
	r = m.run("later", map[string]string{"T": "p"})
	if x, _ := row(r, "T"); x.Quarantined {
		t.Fatalf("one new flaky commit put it back: %+v", x)
	}
}

// A flaky run resets the pass streak, so a test that keeps flaking stays.
func TestFlakyRunKeepsQuarantine(t *testing.T) {
	m := newSim(t)
	quarantined(t, m, "T")
	for i := 0; i < 100; i++ {
		a := "p"
		if i%10 == 9 {
			a = "fp"
		}
		r := m.run(fmt.Sprint("k", i), map[string]string{"T": a})
		if x, _ := row(r, "T"); !x.Quarantined {
			t.Fatalf("run %d: left quarantine while still flaking", i)
		}
	}
}

// Without a commit only a run's own retries count as the same code.
func TestNoCommit(t *testing.T) {
	m := newSim(t)
	m.run("", map[string]string{"T": "f"})
	r := m.run("", map[string]string{"T": "p"})
	if x, _ := row(r, "T"); x.FlakyCommits != 0 {
		t.Fatalf("runs without a commit aren't the same code: %+v", x)
	}
	m.run("", map[string]string{"T": "fp"})
	r = m.run("", map[string]string{"T": "fp"})
	if x, _ := row(r, "T"); !x.Quarantined || x.FlakyCommits != 2 {
		t.Fatalf("in-run retries count: %+v", x)
	}
}

// Flaky commits older than the window are forgotten.
func TestWindow(t *testing.T) {
	m := newSim(t)
	m.s.Window = 10
	m.run("a", map[string]string{"T": "fp"})
	for i := 0; i < 10; i++ {
		m.run(fmt.Sprint("w", i), map[string]string{"T": "p"})
	}
	r := m.run("b", map[string]string{"T": "fp"})
	if x, _ := row(r, "T"); x.Quarantined || x.FlakyCommits != 1 {
		t.Fatalf("the first flaky commit is outside the window: %+v", x)
	}
}

// A quarantined test missing from a run is listed as absent; one missing
// for a whole window is dropped.
func TestAbsent(t *testing.T) {
	m := newSim(t)
	m.s.Window = 5
	quarantined(t, m, "T")
	r := m.run("x", map[string]string{"U": "p"})
	wantStatus(t, r, "T", StatusAbsent, false)
	for i := 0; i < 5; i++ {
		r = m.run(fmt.Sprint("y", i), map[string]string{"U": "p"})
	}
	if _, ok := row(r, "T"); ok || r.InQuarantine != 0 {
		t.Fatalf("a test gone for a window isn't listed: %+v", r)
	}
}

func TestJudgeStateOnly(t *testing.T) {
	m := newSim(t)
	quarantined(t, m, "T")
	m.run("z", map[string]string{"T": "p", "U": "p"})
	r := Judge(m.entries, nil, m.s)
	if r.Verdict != VerdictPass || r.InQuarantine != 1 || len(r.Rows) != 1 || r.Rows[0].Status != StatusAbsent {
		t.Fatalf("%+v", r)
	}
}

func TestOutcome(t *testing.T) {
	for a, want := range map[string]string{"": "", "p": OutPass, "pp": OutPass, "f": OutFail, "ff": OutFail, "fp": OutFlaky, "pf": OutFlaky, "s": OutSkip, "sp": OutPass, "sf": OutFail} {
		if got := Outcome(a); got != want {
			t.Errorf("Outcome(%q) = %q, want %q", a, got, want)
		}
	}
}

// The history file: written and read back, kept to keep lines, damaged
// lines skipped and kept, a missing file empty, a run history refused.
func TestHistoryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tests.jsonl")
	entries, bad, missing, err := Load(path)
	if err != nil || !missing || len(entries) != 0 || bad != 0 {
		t.Fatalf("missing file: %v %v %v %v", entries, bad, missing, err)
	}
	for i := 0; i < 5; i++ {
		e := NewEntry("test", time.Now(), fmt.Sprint("c", i), map[string]string{"T": "p", "U": "fp"})
		if err := Append(path, e, 3); err != nil {
			t.Fatal(err)
		}
	}
	entries, bad, missing, err = Load(path)
	if err != nil || missing || bad != 0 || len(entries) != 3 || entries[0].Commit != "c2" || entries[2].Tests["U"] != "fp" {
		t.Fatalf("got %+v %v %v %v", entries, bad, missing, err)
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", st.Mode())
	}
	tmps, _ := filepath.Glob(filepath.Join(dir, ".tests.jsonl.tmp-*"))
	if len(tmps) != 0 {
		t.Errorf("temp files left: %v", tmps)
	}
}

// A damaged history (a cache restored half way, a line cut short, binary
// junk, someone's edit) loses only the damaged lines; jvq still judges and
// still adds the run.
func TestCorruptedHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tests.jsonl")
	good := func(c, a string) string {
		return fmt.Sprintf(`{"kind":"tests","jvq":"0.7.0","date":"2026-10-31T00:00:00Z","commit":%q,"tests":{"T":%q}}`, c, a)
	}
	content := strings.Join([]string{
		good("a", "fp"),
		"\x00\x01\x02 binary junk \xff\xfe",
		`{"kind":"tests","jvq":"0.7.0","tests":{"T":"zz"}}`, // letters it doesn't know
		`{"kind":"other","tests":{"T":"p"}}`,
		`not json at all`,
		good("b", "fp"),
		`{"kind":"tests","jvq":"0.7.0","date":"2026-10-31T00:00:00Z","commit":"c","tests":{"T":"f`, // cut short
	}, "\n")
	os.WriteFile(path, []byte(content), 0o600)
	entries, bad, _, err := Load(path)
	if err != nil || len(entries) != 2 || bad != 5 {
		t.Fatalf("entries %d bad %d err %v", len(entries), bad, err)
	}
	run := NewEntry("test", time.Now(), "d", map[string]string{"T": "f"})
	r := Judge(entries, &run, DefaultSettings())
	wantStatus(t, r, "T", StatusQuarantined, false) // the two good lines still count
	if err := Append(path, run, 0); err != nil {
		t.Fatal(err)
	}
	entries, bad, _, err = Load(path)
	if err != nil || len(entries) != 3 || bad != 5 {
		t.Fatalf("after append: entries %d bad %d err %v", len(entries), bad, err)
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode not kept: %v", st.Mode()) // Windows has no such modes
	}
	// A file that is nothing but junk is an empty history, not an error.
	junk := filepath.Join(dir, "junk.jsonl")
	os.WriteFile(junk, []byte("\x89PNG\r\n\x1a\n\x00\x00garbage"), 0o644)
	entries, bad, _, err = Load(junk)
	if err != nil || len(entries) != 0 || bad != 3 {
		t.Fatalf("junk: %d %d %v", len(entries), bad, err)
	}
}

func TestRunHistoryRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jvq-history.jsonl")
	line := `{"jvq":"0.6.0","date":"2026-10-19T00:00:00Z","base":{"sha":"a"},"head":{"sha":"b"},"verdict":"pass","results":[]}` + "\n"
	os.WriteFile(path, []byte(line), 0o644)
	if _, _, _, err := Load(path); !errors.Is(err, ErrRunHistory) {
		t.Errorf("Load: %v", err)
	}
	if err := Append(path, NewEntry("t", time.Now(), "c", map[string]string{"T": "p"}), 0); !errors.Is(err, ErrRunHistory) {
		t.Errorf("Append: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != line {
		t.Error("the run history was changed")
	}
	if err := Append(filepath.Join(dir, "no", "such", "dir.jsonl"), NewEntry("t", time.Now(), "c", nil), 0); err == nil {
		t.Error("a missing folder should be an error")
	}
	if _, _, _, err := Load(dir); err == nil {
		t.Error("a folder isn't a history file")
	}
}

// A history of many runs replays quickly: 200 runs of 2000 tests.
func TestReplaySize(t *testing.T) {
	var entries []Entry
	for i := 0; i < 200; i++ {
		tests := map[string]string{}
		for j := 0; j < 2000; j++ {
			a := "p"
			if (i*7+j)%97 == 0 {
				a = "fp"
			}
			tests[fmt.Sprintf("pkg.Test%04d", j)] = a
		}
		entries = append(entries, NewEntry("t", time.Now(), fmt.Sprint("c", i), tests))
	}
	start := time.Now()
	r := Judge(entries[:199], &entries[199], DefaultSettings())
	if d := time.Since(start); d > time.Minute {
		t.Errorf("took %v", d)
	}
	if r.Tests != 2000 || r.InQuarantine == 0 {
		t.Errorf("tests %d in quarantine %d", r.Tests, r.InQuarantine)
	}
}

// Independent review, October 2026: a history line whose test has no
// attempts at all ("") is no evidence. It used to count as both a pass and
// a failure, so two such lines quarantined a test and hid its next real
// failure.
func TestEmptyAttemptsAreNoEvidence(t *testing.T) {
	src := `{"kind":"tests","jvq":"0.7.0","date":"2026-10-30T00:00:00Z","commit":"a","tests":{"p.TestA":""}}
{"kind":"tests","jvq":"0.7.0","date":"2026-10-30T00:00:01Z","commit":"b","tests":{"p.TestA":""}}
`
	entries, _, err := Read(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	run := NewEntry("test", time.Now(), "c", map[string]string{"p.TestA": "f"})
	r := Judge(entries, &run, DefaultSettings())
	wantStatus(t, r, "p.TestA", StatusFailed, true)
}

// Independent review, October 2026: a package-level failure that is flaky
// (a leak check in TestMain) may put "pkg (package)" in quarantine, but a
// build error in that package must still block: it's recorded as
// "pkg (build)", which never passes and so never goes into quarantine.
func TestBuildFailureNeverQuarantined(t *testing.T) {
	m := newSim(t)
	m.run("a", map[string]string{"p (package)": "f"})
	m.run("a", map[string]string{"p (package)": "p"})
	m.run("b", map[string]string{"p (package)": "f"})
	r := m.run("b", map[string]string{"p (package)": "p"})
	if x, _ := row(r, "p (package)"); !x.Quarantined {
		t.Fatalf("setup: p (package) should be in quarantine: %+v", r.Rows)
	}
	r = m.run("c", map[string]string{"p (build)": "f"})
	wantStatus(t, r, "p (build)", StatusFailed, true)
	if r.Verdict != VerdictFail {
		t.Fatalf("a build failure passed the gate: %+v", r)
	}
}

// A quarantined Go test that crashes stops the rest of its package: the
// tests that ran in the package's last run and are missing now are
// reported as not run, so the hole shows. They don't block; the crash is
// in quarantine.
func TestCrashLeavesTestsNotRun(t *testing.T) {
	m := newSim(t)
	const p = "example.com/shop"
	full := map[string]string{p + ".TestFlaky": "p", p + ".TestA": "p", p + ".TestB": "p", p + " (package)": "p",
		p + "/sub.TestC": "p", p + "/sub (package)": "p", "example.com/shop.v2.TestD": "p", "example.com/shop.v2 (package)": "p"}
	flakyRun := func(commit string) {
		run := map[string]string{}
		for k, v := range full {
			run[k] = v
		}
		run[p+".TestFlaky"] = "fp"
		m.run(commit, run)
	}
	flakyRun("a")
	flakyRun("b")
	// Now in quarantine. It crashes: TestB never ran, the package failed.
	r := m.run("c", map[string]string{p + ".TestFlaky": "f", p + ".TestA": "p",
		p + "/sub.TestC": "p", p + "/sub (package)": "p", "example.com/shop.v2 (package)": "p"})
	wantStatus(t, r, p+".TestFlaky", StatusQuarantined, false)
	wantStatus(t, r, p+".TestB", StatusNotRun, false)
	if r.NotRun != 1 || r.Verdict != VerdictPass {
		t.Fatalf("not run %d, verdict %s; rows %+v", r.NotRun, r.Verdict, r.Rows)
	}
	// Tests of other packages that are missing (shop.v2's TestD shares the
	// prefix "example.com/shop.") aren't the crashed package's.
	if _, ok := row(r, "example.com/shop.v2.TestD"); ok {
		t.Fatal("another package's missing test reported as not run")
	}
	// A package that passed is left alone: tests may come and go.
	r = m.run("d", map[string]string{p + ".TestFlaky": "p", p + ".TestA": "p", p + " (package)": "p"})
	if r.NotRun != 0 {
		t.Fatalf("not run in a passing package: %+v", r.Rows)
	}
}

// Review of 0.8: a second crash in a row still reports the hidden tests
// (the baseline is the package's last passing run); subtests of a test
// that failed in this run aren't "not run"; many new failing packages
// stay fast.
func TestNotRunReview(t *testing.T) {
	full := map[string]string{"p (package)": "p", "p.TestA": "p", "p.TestB": "p", "p.TestC": "p", "p.TestD": "p", "p.TestD/x": "p"}
	crash := map[string]string{"p (package)": "s", "p.TestA": "f", "p.TestD": "f"}
	ent := func(c string, m map[string]string) Entry { return Entry{Kind: Kind, Commit: c, Tests: m} }
	h := []Entry{ent("c1", full), ent("c2", crash)}
	r := Judge(h, &Entry{Commit: "c3", Tests: crash}, DefaultSettings())
	if r.NotRun != 2 {
		t.Errorf("second crash: not_run %d, want 2 (TestB, TestC); rows %+v", r.NotRun, r.Rows)
	}
	if _, ok := row(r, "p.TestD/x"); ok {
		t.Error("a subtest of a test that failed now reported as not run")
	}
	var big []Entry
	for e := 0; e < 200; e++ {
		m := map[string]string{}
		for p := 0; p < 100; p++ {
			for k := 0; k < 20; k++ {
				m[fmt.Sprintf("github.com/org/repo/internal/pkg%d.TestSomething%d/sub.case.%d", p, k, k)] = "p"
			}
		}
		big = append(big, ent(fmt.Sprint(e), m))
	}
	run := map[string]string{}
	for p := 0; p < 100; p++ {
		run[fmt.Sprintf("github.com/org/repo/new/pkg%d (package)", p)] = "s"
		run[fmt.Sprintf("github.com/org/repo/new/pkg%d.TestA", p)] = "f"
	}
	start := time.Now()
	notRun(big, Entry{Tests: run})
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("notRun took %v", d)
	}
}
