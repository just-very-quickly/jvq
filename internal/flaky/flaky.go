// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package flaky keeps a history of test results across CI runs, finds the
// tests that pass and fail on the same code, quarantines the ones that do
// it again and again so they stop blocking merges, and lets them go when
// they are stable again.
//
// Nothing but the runs is stored. Each time, the whole history is replayed
// in order, so quarantine is always what the runs say it is: it can't
// drift from them, and a damaged line costs that one run.
//
// A failure on the same commit as a pass is the only evidence of
// flakiness: across commits the code changed, so a test that passed
// yesterday and fails today may well be right. A test whose recent
// attempts all failed is broken, not flaky, and is never quarantined.
package flaky

import (
	"sort"
	"strconv"
	"strings"

	"github.com/just-very-quickly/jvq/internal/testresults"
)

// Settings are the quarantine rules.
type Settings struct {
	// Window is how many of a test's latest runs are searched for flaky
	// commits.
	Window int
	// QuarantineAfter is how many flaky commits within the window put a
	// test in quarantine.
	QuarantineAfter int
	// ReleaseAfter is how many passing runs in a row take a test out.
	ReleaseAfter int
	// BrokenAfter is how many failed attempts in a row make a test broken:
	// out of quarantine, and its failures block.
	BrokenAfter int
}

// Default settings.
const (
	DefaultWindow          = 100
	DefaultQuarantineAfter = 2
	DefaultReleaseAfter    = 30
	DefaultBrokenAfter     = 3
	DefaultKeep            = 200
)

// DefaultSettings returns the default rules.
func DefaultSettings() Settings {
	return Settings{Window: DefaultWindow, QuarantineAfter: DefaultQuarantineAfter, ReleaseAfter: DefaultReleaseAfter, BrokenAfter: DefaultBrokenAfter}
}

// Outcomes of a test in one run.
const (
	OutPass  = "pass"
	OutFail  = "fail"  // no attempt passed
	OutFlaky = "flaky" // failed and passed
	OutSkip  = "skip"
)

// Outcome sums up a run's attempts of a test.
func Outcome(attempts string) string {
	p := strings.IndexByte(attempts, testresults.Pass) >= 0
	f := strings.IndexByte(attempts, testresults.Fail) >= 0
	switch {
	case p && f:
		return OutFlaky
	case f:
		return OutFail
	case p:
		return OutPass
	case attempts == "":
		return ""
	}
	return OutSkip
}

// Events: what a run did to a test's quarantine.
const (
	EventEntered  = "entered"  // into quarantine
	EventReleased = "released" // out, stable again
	EventBroken   = "broken"   // out, failing every attempt
)

// State is one test after replaying the history.
type State struct {
	ID          string
	Quarantined bool
	// Since is the date and commit of the run that put it in quarantine.
	Since, SinceCommit string
	// FlakyCommits counts the commits in its window on which it both
	// passed and failed.
	FlakyCommits int
	// PassStreak is its passing runs in a row; FailStreak its failed
	// attempts in a row.
	PassStreak, FailStreak int
	// Runs counts the runs it was in.
	Runs int
	// Recent has one letter per run, latest last: p pass, f fail,
	// x flaky (failed and passed), s skipped.
	Recent string
	// Event is what the last replayed run did to its quarantine, if
	// anything, and LastRun is that run's index.
	Event   string
	LastRun int

	records  []record // runs in the window since the last clear
	byCommit map[string]*commitCount
}

type record struct {
	commit     string
	pass, fail bool
}

// commitCount counts a commit's runs in the window, to keep FlakyCommits
// up to date as runs come and go.
type commitCount struct{ runs, passes, fails int }

func (c *commitCount) flaky() bool { return c.passes > 0 && c.fails > 0 }

func (st *State) add(r record) {
	if st.byCommit == nil {
		st.byCommit = map[string]*commitCount{}
	}
	c := st.byCommit[r.commit]
	if c == nil {
		c = &commitCount{}
		st.byCommit[r.commit] = c
	}
	was := c.flaky()
	c.runs++
	if r.pass {
		c.passes++
	}
	if r.fail {
		c.fails++
	}
	if !was && c.flaky() {
		st.FlakyCommits++
	}
	st.records = append(st.records, r)
}

func (st *State) evict() {
	r := st.records[0]
	st.records = st.records[1:]
	c := st.byCommit[r.commit]
	was := c.flaky()
	c.runs--
	if r.pass {
		c.passes--
	}
	if r.fail {
		c.fails--
	}
	if was && !c.flaky() {
		st.FlakyCommits--
	}
	if c.runs == 0 {
		delete(st.byCommit, r.commit)
	}
}

func (st *State) clear() {
	st.records, st.byCommit, st.FlakyCommits = nil, nil, 0
}

// recentRuns is how many letters Recent keeps.
const recentRuns = 30

// Replay runs every entry through the rules, oldest first, and returns
// each test's state.
func Replay(entries []Entry, s Settings) map[string]*State {
	states := map[string]*State{}
	for i, e := range entries {
		step(states, i, e, s)
	}
	return states
}

func step(states map[string]*State, i int, e Entry, s Settings) {
	ids := make([]string, 0, len(e.Tests))
	for id := range e.Tests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a := e.Tests[id]
		st := states[id]
		if st == nil {
			st = &State{ID: id}
			states[id] = st
		}
		st.Event = ""
		st.LastRun = i
		st.Runs++
		out := Outcome(a)
		var letter byte
		switch out {
		case OutPass:
			letter = 'p'
		case OutFail:
			letter = 'f'
		case OutFlaky:
			letter = 'x'
		default:
			letter = 's'
		}
		st.Recent += string(letter)
		if len(st.Recent) > recentRuns {
			st.Recent = st.Recent[len(st.Recent)-recentRuns:]
		}
		for _, c := range []byte(a) {
			switch c {
			case testresults.Fail:
				st.FailStreak++
			case testresults.Pass:
				st.FailStreak = 0
			}
		}
		switch out {
		case OutPass:
			st.PassStreak++
		case OutFail, OutFlaky:
			st.PassStreak = 0
		}
		if out == OutSkip || out == "" {
			// Skipped, or no attempts at all (only a damaged or hand-made
			// line has none): no evidence either way.
			continue
		}
		commit := e.Commit
		if commit == "" {
			// Without a commit, only this run's own retries are the same code.
			commit = "#" + strconv.Itoa(i)
		}
		st.add(record{commit: commit, pass: out != OutFail, fail: out != OutPass})
		for len(st.records) > s.Window {
			st.evict()
		}
		switch {
		case st.Quarantined && st.FailStreak >= s.BrokenAfter:
			st.Quarantined, st.Event = false, EventBroken
			st.clear()
		case st.Quarantined && st.PassStreak >= s.ReleaseAfter:
			st.Quarantined, st.Event = false, EventReleased
			st.clear()
		case !st.Quarantined && st.FlakyCommits >= s.QuarantineAfter && st.FailStreak < s.BrokenAfter:
			st.Quarantined, st.Event = true, EventEntered
			st.Since, st.SinceCommit = e.Date, e.Commit
		}
	}
}

// Statuses of a test in a judged run.
const (
	StatusFailed      = "failed"      // failed, not quarantined: blocks
	StatusBroken      = "broken"      // failed every recent attempt: out of quarantine, blocks
	StatusQuarantined = "quarantined" // failed while quarantined: reported, doesn't block
	StatusFlaky       = "flaky"       // failed and passed on retry: doesn't block
	StatusPassed      = "passed"
	StatusSkipped     = "skipped"
	StatusAbsent      = "absent" // quarantined, but not in this run
	// StatusNotRun is a test that ran in its Go package's last run and is
	// missing now that the package failed: a crash (a panic, a timeout)
	// stopped the package before it. It doesn't block; it is a hole the
	// report shows.
	StatusNotRun = "not_run"
)

// Row is one test worth showing: anything but a plain pass outside
// quarantine.
type Row struct {
	ID           string `json:"test"`
	Attempts     string `json:"attempts,omitempty"` // this run's, such as "fp"
	Status       string `json:"status"`
	Blocks       bool   `json:"blocks"`
	Quarantined  bool   `json:"quarantined"` // after this run
	Event        string `json:"event,omitempty"`
	Since        string `json:"quarantined_since,omitempty"`
	FlakyCommits int    `json:"flaky_commits"`
	PassStreak   int    `json:"pass_streak"`
	FailStreak   int    `json:"fail_streak"`
	Runs         int    `json:"runs"`
	Recent       string `json:"recent"`
}

// Verdicts.
const (
	VerdictPass = "pass"
	VerdictFail = "fail"
)

// Result is a judged run.
type Result struct {
	Verdict string `json:"verdict"`
	Tests   int    `json:"tests"` // in this run, not counting Go packages' own entries (package) and (build)
	// Counts of this run's tests by status.
	Failed      int `json:"failed"`
	Broken      int `json:"broken"`
	Quarantined int `json:"quarantined_failures"`
	Flaky       int `json:"flaky"`
	// NotRun counts tests that didn't run because their Go package
	// stopped (StatusNotRun).
	NotRun int `json:"not_run"`
	// InQuarantine counts the tests in quarantine after this run.
	InQuarantine int   `json:"in_quarantine"`
	Rows         []Row `json:"tests_of_note"`
}

// Judge replays the history with the current run as its newest entry and
// decides the run. A test that failed (no attempt passed) blocks unless it
// is in quarantine after this run; a broken test is never in quarantine.
// A test that failed and then passed on retry doesn't block, but its
// failure counts towards quarantine. With run == nil it reports the
// history's state alone.
func Judge(entries []Entry, run *Entry, s Settings) Result {
	all := entries
	cur := -1
	if run != nil {
		all = append(append([]Entry(nil), entries...), *run)
		cur = len(all) - 1
	}
	states := Replay(all, s)
	res := Result{Verdict: VerdictPass}
	if run != nil {
		for id := range run.Tests {
			if testresults.IsTest(id) {
				res.Tests++
			}
		}
	}
	for _, st := range states {
		var a string
		if run != nil {
			a = run.Tests[st.ID]
		}
		inRun := run != nil && st.LastRun == cur
		r := Row{ID: st.ID, Attempts: a, Quarantined: st.Quarantined, Since: st.Since,
			FlakyCommits: st.FlakyCommits, PassStreak: st.PassStreak, FailStreak: st.FailStreak, Runs: st.Runs, Recent: st.Recent}
		if inRun {
			r.Event = st.Event
		}
		if !st.Quarantined {
			r.Since = ""
		}
		switch {
		case !inRun:
			// A quarantined test that hasn't run for a whole window was
			// most likely renamed or deleted; it isn't listed.
			if !st.Quarantined || len(all)-1-st.LastRun >= s.Window {
				continue
			}
			r.Status = StatusAbsent
		default:
			switch Outcome(a) {
			case OutFail:
				switch {
				case st.Quarantined:
					r.Status = StatusQuarantined
					res.Quarantined++
				case st.Event == EventBroken:
					r.Status, r.Blocks = StatusBroken, true
					res.Broken++
				default:
					r.Status, r.Blocks = StatusFailed, true
					res.Failed++
				}
			case OutFlaky:
				r.Status = StatusFlaky
				res.Flaky++
			case OutSkip:
				r.Status = StatusSkipped
			default:
				r.Status = StatusPassed
			}
			if r.Status == StatusPassed || r.Status == StatusSkipped {
				if !st.Quarantined && r.Event == "" {
					continue
				}
			}
		}
		if st.Quarantined {
			res.InQuarantine++
		}
		res.Rows = append(res.Rows, r)
	}
	if run != nil {
		quarantined := func(id string) bool { st := states[id]; return st != nil && st.Quarantined }
		for _, id := range notRun(entries, *run, quarantined) {
			st := states[id]
			r := Row{ID: id, Status: StatusNotRun}
			if st != nil {
				r.Quarantined, r.FlakyCommits, r.PassStreak, r.FailStreak, r.Runs, r.Recent = st.Quarantined, st.FlakyCommits, st.PassStreak, st.FailStreak, st.Runs, st.Recent
				if st.Quarantined {
					// Listed as absent already; this says why.
					for i := range res.Rows {
						if res.Rows[i].ID == id {
							res.Rows[i].Status = StatusNotRun
							res.NotRun++
						}
					}
					continue
				}
			}
			res.NotRun++
			res.Rows = append(res.Rows, r)
		}
	}
	if res.Failed+res.Broken > 0 {
		res.Verdict = VerdictFail
	}
	sort.Slice(res.Rows, func(i, j int) bool {
		a, b := res.Rows[i], res.Rows[j]
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		return a.ID < b.ID
	})
	return res
}

func rank(r Row) int {
	switch r.Status {
	case StatusBroken:
		return 0
	case StatusFailed:
		return 1
	case StatusQuarantined:
		return 2
	case StatusNotRun:
		return 3
	case StatusFlaky:
		return 4
	}
	if r.Event != "" {
		return 5
	}
	return 6
}

// allPass reports attempts that are all passes.
func allPass(a string) bool {
	return a != "" && strings.Trim(a, string(testresults.Pass)) == ""
}

// notRun lists the tests of the run's failed Go packages that ran in the
// package's last passing run and are missing now: a crash (a panic, a
// timeout) or -failfast stopped the package before them. The last passing
// run, not the last run, so a second crash in a row still shows them. A
// Go package is known by its "pkg (package)" or "pkg (build)" entry; a test
// belongs to the longest known package its name starts with, followed by
// a dot. A package that failed to build is left out (its build failure
// blocks on its own), and so are subtests of a test that failed in this
// run (it stopped before them, which its failure already says).
func notRun(entries []Entry, run Entry, quarantined func(string) bool) []string {
	pkgs := map[string]bool{}
	note := func(tests map[string]string) {
		for id := range tests {
			if p, ok := strings.CutSuffix(id, testresults.PackageSuffix); ok {
				pkgs[p] = true
			} else if p, ok := strings.CutSuffix(id, testresults.BuildSuffix); ok {
				pkgs[p] = true
			}
		}
	}
	note(run.Tests)
	for _, e := range entries {
		note(e.Tests)
	}
	owners := map[string]string{}
	owner := func(id string) string {
		if o, ok := owners[id]; ok {
			return o
		}
		best := ""
		for i := strings.IndexByte(id, '.'); i >= 0; {
			if p := id[:i]; pkgs[p] {
				best = p
			}
			j := strings.IndexByte(id[i+1:], '.')
			if j < 0 {
				break
			}
			i += 1 + j
		}
		owners[id] = best
		return best
	}
	// The packages of this run that failed: a test of theirs ran, or
	// their own entry is there, and that entry isn't all passes (a rerun
	// that passed after a crash is "sp": the first run still stopped). A
	// build failure isn't one of them.
	failed := map[string]bool{}
	for id := range run.Tests {
		if p, ok := strings.CutSuffix(id, testresults.PackageSuffix); ok {
			failed[p] = true
		} else if testresults.IsTest(id) {
			if p := owner(id); p != "" {
				failed[p] = true
			}
		}
	}
	for p := range failed {
		if a, ok := run.Tests[p+testresults.PackageSuffix]; ok && allPass(a) {
			delete(failed, p)
		}
		if _, ok := run.Tests[p+testresults.BuildSuffix]; ok {
			delete(failed, p)
		}
	}
	if len(failed) == 0 {
		return nil
	}
	// Each failed package's last passing run, newest first.
	base := map[string]int{}
	for i := len(entries) - 1; i >= 0 && len(base) < len(failed); i-- {
		for p := range failed {
			if _, done := base[p]; done {
				continue
			}
			if a, ok := entries[i].Tests[p+testresults.PackageSuffix]; ok && allPass(a) {
				base[p] = i
			}
		}
	}
	// A test that failed here and blocks says it stopped; one in
	// quarantine doesn't block, so what it stopped is listed.
	failedHere := func(id string) bool {
		a, ok := run.Tests[id]
		return ok && strings.IndexByte(a, testresults.Fail) >= 0 && (quarantined == nil || !quarantined(id))
	}
	var out []string
	for p, i := range base {
		for id, a := range entries[i].Tests {
			if !strings.HasPrefix(id, p+".") || !testresults.IsTest(id) || Outcome(a) == OutSkip || Outcome(a) == "" || owner(id) != p {
				continue
			}
			if _, ok := run.Tests[id]; ok {
				continue
			}
			parentFailed := false
			name := id[len(p)+1:]
			for j := strings.IndexByte(name, '/'); j >= 0; {
				if failedHere(p + "." + name[:j]) {
					parentFailed = true
					break
				}
				k := strings.IndexByte(name[j+1:], '/')
				if k < 0 {
					break
				}
				j += 1 + k
			}
			if !parentFailed {
				out = append(out, id)
			}
		}
	}
	sort.Strings(out)
	return out
}
