// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Command flaky measures jvq's flaky test gate on simulated CI histories
// where the truth is known: which tests are flaky and how often they fail,
// which are stable, and when one breaks for real or gets fixed.
//
// Each CI run writes real result files, half the suite as go test -json
// and half as JUnit XML, and runs the real jvq binary's `jvq flaky` on
// them with a history file kept between runs, the way a pipeline would.
// The scenarios:
//
//   - flaky suite: 300 commits of a 200-test suite with 5 flaky tests
//     (failing 2, 5, 10, 20 and 30% of attempts), no retries; a red run is
//     re-run once on the same commit, as people do.
//   - breaks, no retries: a quarantined flaky test starts failing for
//     real; how many runs until it blocks. A stable test breaking beside
//     it is the control.
//   - breaks, with retries: the same with each failed test run twice more
//     (gotestsum --rerun-fails=2 style).
//   - fixed: a quarantined test is fixed; how many runs until it leaves.
//   - damaged history: the history file is garbled half way and later
//     deleted; does the gate keep working.
//
// It writes test/results/flaky.json and flaky.md. scripts/record.sh runs it.
//
//	go run ./test/flaky -out test/results
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

var bin string

func main() {
	out := flag.String("out", "test/results", "folder to write flaky.json and flaky.md to")
	trials := flag.Int("trials", 10, "trials of the flaky suite, fixed and damaged history scenarios")
	breakTrials := flag.Int("break-trials", 50, "trials of each breaking scenario")
	flag.Parse()
	start := time.Now()
	tmp, err := os.MkdirTemp("", "jvq-flaky-")
	check(err)
	defer os.RemoveAll(tmp)
	bin = filepath.Join(tmp, "jvq")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if b, err := exec.Command("go", "build", "-o", bin, "./cmd/jvq").CombinedOutput(); err != nil {
		fail("building jvq: %v\n%s", err, b)
	}
	v, err := exec.Command(bin, "version").Output()
	check(err)

	o := output{Date: time.Now().UTC().Format("2006-01-02"), JVQ: strings.Fields(string(v))[1], Machine: machine()}
	o.Suite = flakySuite(tmp, *trials)
	o.BreakNoRetries = breaking(tmp, *breakTrials, 0)
	o.BreakRetries = breaking(tmp, *breakTrials, 2)
	o.Fixed = fixed(tmp, *trials)
	o.Damaged = damaged(tmp, *trials)
	o.TotalSecs = time.Since(start).Seconds()

	check(os.MkdirAll(*out, 0o755))
	b, err := json.MarshalIndent(o, "", "  ")
	check(err)
	check(os.WriteFile(filepath.Join(*out, "flaky.json"), append(b, '\n'), 0o644))
	check(os.WriteFile(filepath.Join(*out, "flaky.md"), []byte(markdown(o)), 0o644))
	fmt.Fprintf(os.Stderr, "flaky: done in %s\n", time.Since(start).Round(time.Second))
}

type output struct {
	Date           string         `json:"date"`
	JVQ            string         `json:"jvq"`
	Machine        map[string]any `json:"machine"`
	Suite          suiteResult    `json:"flaky_suite"`
	BreakNoRetries breakResult    `json:"breaks_no_retries"`
	BreakRetries   breakResult    `json:"breaks_with_retries"`
	Fixed          fixedResult    `json:"fixed"`
	Damaged        damagedResult  `json:"damaged_history"`
	TotalSecs      float64        `json:"total_seconds"`
}

// A test in the simulated suite.
type test struct {
	id   string
	rate float64 // chance an attempt fails
}

const stableTests = 200

var flakyRates = []float64{0.02, 0.05, 0.10, 0.20, 0.30}

func suite() []test {
	var ts []test
	for i := 0; i < stableTests; i++ {
		ts = append(ts, test{id: name(i%2 == 0, fmt.Sprintf("Stable%03d", i))})
	}
	for i, r := range flakyRates {
		ts = append(ts, test{id: name(i%2 == 0, fmt.Sprintf("Flaky%02.0f", r*100)), rate: r})
	}
	return ts
}

// name puts a test in the go test -json half (example.com/app.TestX) or
// the JUnit half (web.Suite.x) of the results.
func name(goHalf bool, n string) string {
	if goHalf {
		return "example.com/app.Test" + n
	}
	return "web.Suite." + strings.ToLower(n[:1]) + n[1:]
}

// attempts runs a test: each failed attempt is retried up to retries
// times. broken tests always fail.
func attempts(rng *rand.Rand, rate float64, retries int) string {
	var a strings.Builder
	for i := 0; i <= retries; i++ {
		if rate >= 1 || rng.Float64() < rate {
			a.WriteByte('f')
			continue
		}
		a.WriteByte('p')
		break
	}
	return a.String()
}

func sample(rng *rand.Rand, ts []test, retries int) map[string]string {
	m := map[string]string{}
	for _, t := range ts {
		m[t.id] = attempts(rng, t.rate, retries)
	}
	return m
}

// red reports whether a run fails without quarantine: some test failed
// every attempt.
func red(m map[string]string) bool {
	for _, a := range m {
		if !strings.Contains(a, "p") {
			return true
		}
	}
	return false
}

// ci holds one simulated pipeline's files.
type ci struct {
	dir, hist string
}

func newCI(tmp, label string) *ci {
	dir, err := os.MkdirTemp(tmp, label+"-")
	check(err)
	return &ci{dir: dir, hist: filepath.Join(dir, "jvq-tests.jsonl")}
}

type report struct {
	Notes  []string `json:"notes"`
	Result struct {
		Verdict      string `json:"verdict"`
		InQuarantine int    `json:"in_quarantine"`
		Rows         []struct {
			Test        string `json:"test"`
			Status      string `json:"status"`
			Blocks      bool   `json:"blocks"`
			Quarantined bool   `json:"quarantined"`
			Event       string `json:"event"`
		} `json:"tests_of_note"`
	} `json:"result"`
}

func (r report) quarantined() map[string]bool {
	m := map[string]bool{}
	for _, x := range r.Result.Rows {
		if x.Quarantined {
			m[x.Test] = true
		}
	}
	return m
}

func (r report) blocks(id string) bool {
	for _, x := range r.Result.Rows {
		if x.Test == id {
			return x.Blocks
		}
	}
	return false
}

// run writes the results and runs jvq flaky; it returns the exit code and
// the JSON report.
func (c *ci) run(commit string, tests map[string]string) (int, report) {
	goPath := filepath.Join(c.dir, "go-test.json")
	xmlPath := filepath.Join(c.dir, "junit.xml")
	check(os.WriteFile(goPath, []byte(goJSON(tests)), 0o644))
	check(os.WriteFile(xmlPath, []byte(junit(tests)), 0o644))
	jsonPath := filepath.Join(c.dir, "report.json")
	os.Remove(jsonPath)
	cmd := exec.Command(bin, "flaky", "-q", "-history", c.hist, "-commit", commit, "-json", jsonPath, goPath, xmlPath)
	cmd.Dir = c.dir
	cmd.Env = cleanEnv()
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		fail("jvq flaky: %v", err)
	}
	var r report
	if b, err := os.ReadFile(jsonPath); err == nil {
		check(json.Unmarshal(b, &r))
	} else if code != 2 {
		fail("no report (exit %d): %s", code, out)
	}
	return code, r
}

func goJSON(tests map[string]string) string {
	var b strings.Builder
	b.WriteString(`{"Action":"start","Package":"example.com/app"}` + "\n")
	failed := false
	for _, id := range sorted(tests) {
		if !strings.HasPrefix(id, "example.com/app.") {
			continue
		}
		t := strings.TrimPrefix(id, "example.com/app.")
		for _, a := range tests[id] {
			act := "pass"
			if a == 'f' {
				act, failed = "fail", true
			}
			fmt.Fprintf(&b, `{"Action":"run","Package":"example.com/app","Test":%q}`+"\n", t)
			fmt.Fprintf(&b, `{"Action":%q,"Package":"example.com/app","Test":%q,"Elapsed":0.01}`+"\n", act, t)
		}
	}
	end := "pass"
	if failed {
		end = "fail"
	}
	fmt.Fprintf(&b, `{"Action":%q,"Package":"example.com/app","Elapsed":1.5}`+"\n", end)
	return b.String()
}

func junit(tests map[string]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<testsuites><testsuite name=\"web\">\n")
	for _, id := range sorted(tests) {
		if !strings.HasPrefix(id, "web.Suite.") {
			continue
		}
		n := strings.TrimPrefix(id, "web.Suite.")
		for _, a := range tests[id] {
			if a == 'f' {
				fmt.Fprintf(&b, `  <testcase classname="web.Suite" name=%q time="0.2"><failure message="timed out">...</failure></testcase>`+"\n", n)
			} else {
				fmt.Fprintf(&b, `  <testcase classname="web.Suite" name=%q time="0.2"/>`+"\n", n)
			}
		}
	}
	b.WriteString("</testsuite></testsuites>\n")
	return b.String()
}

func sorted(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// --- flaky suite ---

type suiteResult struct {
	Trials         int     `json:"trials"`
	Commits        int     `json:"commits_per_trial"`
	TotalCommits   int     `json:"total_commits"`
	RedWithout     int     `json:"first_runs_red_without_quarantine"`
	RedWith        int     `json:"first_runs_red_with_jvq"`
	RedAfterRerun  int     `json:"commits_still_red_after_one_rerun_with_jvq"`
	Reruns         int     `json:"reruns_with_jvq"`
	StableQuar     int     `json:"stable_tests_ever_quarantined"`
	Errors         int     `json:"exit_2"`
	Entered        []rate  `json:"flaky_tests"`
	RedLast100With int     `json:"first_runs_red_in_last_100_commits_with_jvq"`
	RedLast100W    int     `json:"first_runs_red_in_last_100_commits_without"`
	Seconds        float64 `json:"seconds"`
}

type rate struct {
	Rate        float64 `json:"fail_rate"`
	Quarantined int     `json:"trials_quarantined_by_the_end"`
	MedianRun   int     `json:"median_commit_entered"` // of trials where it entered
	RedCaused   int     `json:"first_runs_it_failed"`
	Blocked     int     `json:"first_runs_it_blocked"`
}

func flakySuite(tmp string, trials int) suiteResult {
	start := time.Now()
	const commits = 300
	ts := suite()
	res := suiteResult{Trials: trials, Commits: commits, TotalCommits: trials * commits}
	entered := make([][]int, len(flakyRates))
	res.Entered = make([]rate, len(flakyRates))
	for i, r := range flakyRates {
		res.Entered[i].Rate = r
	}
	for tr := 0; tr < trials; tr++ {
		rng := rand.New(rand.NewSource(int64(1000 + tr)))
		c := newCI(tmp, "suite")
		first := map[string]int{}
		var last report
		for k := 0; k < commits; k++ {
			commit := fmt.Sprintf("t%dc%03d", tr, k)
			m := sample(rng, ts, 0)
			if red(m) {
				res.RedWithout++
				if k >= commits-100 {
					res.RedLast100W++
				}
			}
			code, r := c.run(commit, m)
			for i := range flakyRates {
				id := ts[stableTests+i].id
				if m[id] == "f" {
					res.Entered[i].RedCaused++
					if r.blocks(id) {
						res.Entered[i].Blocked++
					}
				}
			}
			if code == 2 {
				res.Errors++
			}
			if code == 1 {
				res.RedWith++
				if k >= commits-100 {
					res.RedLast100With++
				}
				res.Reruns++
				code, r = c.run(commit, sample(rng, ts, 0))
				if code == 1 {
					res.RedAfterRerun++
				}
			}
			for id := range r.quarantined() {
				if _, ok := first[id]; !ok {
					first[id] = k
				}
				if strings.Contains(id, "Stable") || strings.Contains(id, "stable") {
					res.StableQuar++
				}
			}
			last = r
		}
		q := last.quarantined()
		for i := range flakyRates {
			id := ts[stableTests+i].id
			if q[id] {
				res.Entered[i].Quarantined++
			}
			if k, ok := first[id]; ok {
				entered[i] = append(entered[i], k)
			}
		}
		fmt.Fprintf(os.Stderr, "flaky suite: trial %d/%d done\n", tr+1, trials)
	}
	for i := range flakyRates {
		res.Entered[i].MedianRun = median(entered[i])
	}
	res.Seconds = time.Since(start).Seconds()
	return res
}

// --- breaking ---

type breakResult struct {
	Trials   int         `json:"trials"`
	Retries  int         `json:"retries"`
	Setup    int         `json:"trials_quarantined_before_breaking"`
	RunsDist map[int]int `json:"runs_until_blocked"` // runs after breaking, counting the first, until the broken quarantined test blocked
	Never    int         `json:"never_blocked"`
	Control  int         `json:"stable_test_blocked_in_first_run"`
	// FalseBroken counts runs before the break where the flaky test failed
	// every attempt while quarantined and was called broken.
	FalseBroken int     `json:"runs_before_break_flaky_called_broken"`
	RunsBefore  int     `json:"runs_before_break"`
	Seconds     float64 `json:"seconds"`
}

func breaking(tmp string, trials, retries int) breakResult {
	start := time.Now()
	res := breakResult{Trials: trials, Retries: retries, RunsDist: map[int]int{}}
	const flakyID = "example.com/app.TestNetwork"
	const stableID = "web.Suite.checkout"
	others := []test{{id: "example.com/app.TestParse"}, {id: "web.Suite.login"}}
	for tr := 0; tr < trials; tr++ {
		rng := rand.New(rand.NewSource(int64(5000 + tr + 1000*retries)))
		c := newCI(tmp, "break")
		k := 0
		run := func(flakyRate, stableRate float64, again bool) (int, report) {
			m := sample(rng, others, retries)
			m[flakyID] = attempts(rng, flakyRate, retries)
			m[stableID] = attempts(rng, stableRate, retries)
			if !again {
				k++
			}
			return c.run(fmt.Sprintf("b%d-%d", tr, k), m)
		}
		// Flaky at 30% until it's in quarantine, then five more runs; a
		// red run is re-run once on the same commit.
		in, after := false, 0
		for k < 200 && after < 5 {
			code, r := run(0.3, 0, false)
			res.RunsBefore++
			if in && r.blocks(flakyID) {
				res.FalseBroken++
			}
			if code == 1 {
				_, r = run(0.3, 0, true)
			}
			in = r.quarantined()[flakyID]
			if in {
				after++
			}
		}
		if !in {
			continue
		}
		res.Setup++
		// Both break for real.
		blocked := 0
		for n := 1; n <= 10; n++ {
			_, r := run(1, 1, false)
			if n == 1 && r.blocks(stableID) {
				res.Control++
			}
			if r.blocks(flakyID) {
				blocked = n
				break
			}
		}
		if blocked == 0 {
			res.Never++
		} else {
			res.RunsDist[blocked]++
		}
	}
	res.Seconds = time.Since(start).Seconds()
	fmt.Fprintf(os.Stderr, "breaking (retries %d): done\n", retries)
	return res
}

// --- fixed ---

type fixedResult struct {
	Trials   int         `json:"trials"`
	Setup    int         `json:"trials_quarantined_before_fix"`
	RunsDist map[int]int `json:"runs_until_released"`
	Seconds  float64     `json:"seconds"`
}

func fixed(tmp string, trials int) fixedResult {
	start := time.Now()
	res := fixedResult{Trials: trials, RunsDist: map[int]int{}}
	const id = "web.Suite.upload"
	for tr := 0; tr < trials; tr++ {
		rng := rand.New(rand.NewSource(int64(9000 + tr)))
		c := newCI(tmp, "fixed")
		k := 0
		run := func(rate float64) report {
			k++
			commit := fmt.Sprintf("f%d-%d", tr, k)
			code, r := c.run(commit, map[string]string{id: attempts(rng, rate, 0), "example.com/app.TestOK": "p"})
			if code == 1 { // re-run a red run once
				_, r = c.run(commit, map[string]string{id: attempts(rng, rate, 0), "example.com/app.TestOK": "p"})
			}
			return r
		}
		in := false
		for k < 200 && !in {
			in = run(0.2).quarantined()[id]
		}
		if !in {
			continue
		}
		res.Setup++
		for n := 1; n <= 100; n++ {
			if !run(0).quarantined()[id] {
				res.RunsDist[n]++
				break
			}
		}
	}
	res.Seconds = time.Since(start).Seconds()
	fmt.Fprintln(os.Stderr, "fixed: done")
	return res
}

// --- damaged history ---

type damagedResult struct {
	Trials            int     `json:"trials"`
	Errors            int     `json:"exit_2"`
	NoteAfterDamage   int     `json:"trials_with_a_note_after_damage"`
	KeptQuarantine    int     `json:"trials_quarantine_unchanged_by_damage"`
	QuarantinedBefore int     `json:"quarantined_before_damage"`
	RestartNote       int     `json:"trials_with_a_note_after_deletion"`
	ReQuarantined     int     `json:"flaky_tests_back_in_quarantine_50_commits_after_deletion"`
	Seconds           float64 `json:"seconds"`
}

func damaged(tmp string, trials int) damagedResult {
	start := time.Now()
	res := damagedResult{Trials: trials}
	ts := suite()
	for tr := 0; tr < trials; tr++ {
		rng := rand.New(rand.NewSource(int64(7000 + tr)))
		c := newCI(tmp, "damaged")
		var before map[string]bool
		for k := 0; k < 200; k++ {
			switch k {
			case 100:
				// A cache restored half way: junk in the middle, the last
				// line cut short.
				b, err := os.ReadFile(c.hist)
				check(err)
				lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
				mid := len(lines) / 2
				lastLine := lines[len(lines)-1]
				lines[len(lines)-1] = lastLine[:len(lastLine)/2]
				lines = append(lines[:mid], append([]string{"\x00\x00\x1f\x8b binary junk"}, lines[mid:]...)...)
				check(os.WriteFile(c.hist, []byte(strings.Join(lines, "\n")), 0o644))
			case 150:
				os.Remove(c.hist) // the cache expired
			}
			code, r := c.run(fmt.Sprintf("d%d-%d", tr, k), sample(rng, ts, 0))
			if code == 2 {
				res.Errors++
			}
			notes := strings.Join(r.Notes, " ") // of the first run, before any re-run
			if code == 1 {
				code, r = c.run(fmt.Sprintf("d%d-%d", tr, k), sample(rng, ts, 0))
				if code == 2 {
					res.Errors++
				}
			}
			switch k {
			case 99:
				before = r.quarantined()
				res.QuarantinedBefore += len(before)
			case 100:
				if strings.Contains(notes, "damaged line") {
					res.NoteAfterDamage++
				}
				// The damaged lines are lost runs, so a test that had just
				// gone in on the last line may drop out; count trials
				// where the quarantine is the same.
				if equal(before, r.quarantined()) {
					res.KeptQuarantine++
				}
			case 150:
				if strings.Contains(notes, "starts the history") {
					res.RestartNote++
				}
			case 199:
				res.ReQuarantined += len(r.quarantined())
			}
		}
	}
	res.Seconds = time.Since(start).Seconds()
	fmt.Fprintln(os.Stderr, "damaged history: done")
	return res
}

func equal(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// --- output ---

func markdown(o output) string {
	var b strings.Builder
	s := o.Suite
	fmt.Fprintf(&b, "# jvq flaky test gate\n\nRecorded %s with jvq %s (%s, %s) on %s, %v CPUs. Total time %s.\n\n",
		o.Date, o.JVQ, o.Machine["go"], o.Machine["os"], o.Machine["cpu"], o.Machine["cpus"], (time.Duration(o.TotalSecs) * time.Second).String())
	fmt.Fprintf(&b, "Simulated CI histories with a known truth. Every run writes real result files, half the tests as go test -json and half as JUnit XML, and runs `jvq flaky` with the default settings and a history file kept between runs.\n\n")

	fmt.Fprintf(&b, "## A suite with flaky tests\n\n%d trials of %d commits each (%d commits in all). The suite has %d stable tests and %d flaky ones, failing %s of their attempts; no retries. When a run is red, it's re-run once on the same commit, as people do.\n\n",
		s.Trials, s.Commits, s.TotalCommits, stableTests, len(flakyRates), ratesText())
	fmt.Fprintln(&b, "| | Without quarantine | With jvq flaky |")
	fmt.Fprintln(&b, "|---|---:|---:|")
	fmt.Fprintf(&b, "| First runs that were red | %d of %d (%s) | %d (%s) |\n", s.RedWithout, s.TotalCommits, pct(s.RedWithout, s.TotalCommits), s.RedWith, pct(s.RedWith, s.TotalCommits))
	fmt.Fprintf(&b, "| Red first runs in the last 100 commits of each trial | %d of %d (%s) | %d (%s) |\n", s.RedLast100W, 100*s.Trials, pct(s.RedLast100W, 100*s.Trials), s.RedLast100With, pct(s.RedLast100With, 100*s.Trials))
	fmt.Fprintf(&b, "| Commits still red after one re-run | | %d |\n", s.RedAfterRerun)
	fmt.Fprintf(&b, "| Stable tests ever quarantined | | %d |\n", s.StableQuar)
	fmt.Fprintf(&b, "| Runs that ended in an error (exit 2) | | %d |\n\n", s.Errors)
	fmt.Fprintln(&b, "| Flaky test fails | First runs it failed | Of those, runs it blocked | Trials in quarantine at the end | Median commit it went in |")
	fmt.Fprintln(&b, "|---:|---:|---:|---:|---:|")
	for _, r := range s.Entered {
		fmt.Fprintf(&b, "| %.0f%% of attempts | %d | %d | %d of %d | %d |\n", r.Rate*100, r.RedCaused, r.Blocked, r.Quarantined, s.Trials, r.MedianRun)
	}
	fmt.Fprintln(&b, "\nA flaky test blocks until it has failed and passed on two commits (here: a red run and its green re-run). The rarer its failures, the longer that takes.")

	for _, br := range []breakResult{o.BreakNoRetries, o.BreakRetries} {
		title := "## A quarantined test breaks for real, no retries"
		if br.Retries > 0 {
			title = fmt.Sprintf("## A quarantined test breaks for real, %d retries", br.Retries)
		}
		fmt.Fprintf(&b, "\n%s\n\n%d trials. A test failing 30%% of its attempts runs until it's in quarantine and five runs more (a red run is re-run once on the same commit); then it fails every attempt, and so does a stable test beside it (the control).", title, br.Trials)
		if br.Retries > 0 {
			fmt.Fprintf(&b, " Each failed test is run %d more times in the same run.", br.Retries)
		}
		fmt.Fprintf(&b, "\n\n| | Trials |\n|---|---:|\n| In quarantine before breaking | %d |\n", br.Setup)
		var ks []int
		for k := range br.RunsDist {
			ks = append(ks, k)
		}
		sort.Ints(ks)
		for _, k := range ks {
			fmt.Fprintf(&b, "| Broken test blocked in run %d after breaking | %d |\n", k, br.RunsDist[k])
		}
		fmt.Fprintf(&b, "| Never blocked (within 10 runs) | %d |\n| Stable test blocked in the first run | %d |\n", br.Never, br.Control)
		fmt.Fprintf(&b, "| Runs before the break where the quarantined flaky test failed every attempt and blocked as broken | %d of %d |\n", br.FalseBroken, br.RunsBefore)
	}

	f := o.Fixed
	fmt.Fprintf(&b, "\n## A quarantined test is fixed\n\n%d trials. A test failing 20%% of its attempts runs until it's in quarantine (a red run is re-run once on the same commit), then always passes.\n\n| | Trials |\n|---|---:|\n| In quarantine before the fix | %d |\n", f.Trials, f.Setup)
	var ks []int
	for k := range f.RunsDist {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	for _, k := range ks {
		fmt.Fprintf(&b, "| Left quarantine on passing run %d after the fix | %d |\n", k, f.RunsDist[k])
	}
	fmt.Fprintf(&b, "\nThe rule is %d passing runs in a row, and a pass just before the fix (the green re-run that put it in quarantine) counts.\n", 30)

	d := o.Damaged
	fmt.Fprintf(&b, "\n## A damaged and a lost history\n\n%d trials of 200 commits of the flaky suite. At commit 100 the history gets a line of binary junk in the middle and its last line cut in half (a cache restored half way); at commit 150 it's deleted (the cache expired).\n\n", d.Trials)
	fmt.Fprintf(&b, "| | Result |\n|---|---:|\n| Runs that ended in an error (exit 2) | %d |\n| Trials with a note about damaged lines | %d of %d |\n| Trials whose quarantine was the same after the damage | %d of %d |\n| Tests in quarantine before the damage, all trials | %d |\n| Trials with a note that the history starts again | %d of %d |\n| Tests back in quarantine 50 commits after the deletion, all trials | %d |\n",
		d.Errors, d.NoteAfterDamage, d.Trials, d.KeptQuarantine, d.Trials, d.QuarantinedBefore, d.RestartNote, d.Trials, d.ReQuarantined)
	return b.String()
}

func ratesText() string {
	var p []string
	for _, r := range flakyRates {
		p = append(p, fmt.Sprintf("%.0f%%", r*100))
	}
	return strings.Join(p[:len(p)-1], ", ") + " and " + p[len(p)-1]
}

func pct(n, d int) string {
	if d == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(d))
}

func median(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int(nil), xs...)
	sort.Ints(s)
	return s[len(s)/2]
}

func machine() map[string]any {
	m := map[string]any{"os": runtime.GOOS + "/" + runtime.GOARCH, "cpus": runtime.NumCPU(), "go": runtime.Version(), "cpu": "unknown CPU"}
	if out, err := exec.Command("go", "env", "GOVERSION").Output(); err == nil {
		m["go"] = strings.TrimSpace(string(out))
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				m["cpu"] = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				break
			}
		}
	}
	return m
}

func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GITHUB_") && !strings.HasPrefix(kv, "CI_") {
			out = append(out, kv)
		}
	}
	return out
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "flaky: "+format+"\n", a...)
	os.Exit(1)
}
