// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Command accuracy measures how often jvq gets the answer right, by running
// the real jvq binary many times on fixture repositories where the true
// answer is known:
//
//   - A/A: head changes nothing measured (a comment). Any regression verdict
//     is a false alarm.
//   - A/B: head does p% more work per op, for several p. With the default 5%
//     budget, slowdowns above it should be flagged and ones at or below it
//     shouldn't.
//   - allocations: head makes one more heap allocation per op, with an
//     allocs budget of 0; and a no-change control.
//
// It writes test/results/accuracy.json (every trial) and accuracy.md (the
// tables the README and the site quote). scripts/record.sh runs it.
//
//	go run ./test/accuracy -out test/results
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/just-very-quickly/jvq/internal/fixture"
)

type trial struct {
	Kind    string   `json:"kind"`  // aa, ab, alloc, alloc-aa
	Level   float64  `json:"level"` // injected slowdown, 0.10 = +10% work
	Exit    int      `json:"exit"`
	Verdict string   `json:"verdict"` // the BenchmarkWork row's verdict
	Change  *float64 `json:"change,omitempty"`
	Lo      *float64 `json:"ci_lo,omitempty"`
	Hi      *float64 `json:"ci_hi,omitempty"`
	Rounds  int      `json:"rounds"` // rounds per side that ran, extra ones included
	Seconds float64  `json:"seconds"`
}

type output struct {
	Date      string         `json:"date"`
	JVQ       string         `json:"jvq"`
	Machine   map[string]any `json:"machine"`
	Settings  map[string]any `json:"settings"`
	Trials    []trial        `json:"trials"`
	Summary   []summaryRow   `json:"summary"`
	TotalSecs float64        `json:"total_seconds"`
}

type summaryRow struct {
	Test       string  `json:"test"`
	Injected   string  `json:"injected"`
	Runs       int     `json:"runs"`
	Regression int     `json:"regression"`
	Pass       int     `json:"pass"`
	Inconcl    int     `json:"inconclusive"`
	MedChange  float64 `json:"median_change"`
	MedRounds  int     `json:"median_rounds"`
	Expected   string  `json:"expected"`
}

func main() {
	var (
		aa        = flag.Int("aa", 200, "no-change runs")
		ab        = flag.Int("ab", 50, "runs per injected slowdown")
		levels    = flag.String("levels", "0.02,0.05,0.10,0.20,0.50", "injected slowdowns")
		allocs    = flag.Int("allocs", 100, "runs for the allocation gate, and as many no-change controls")
		rounds    = flag.Int("rounds", 10, "jvq rounds per side to start with")
		maxRounds = flag.Int("max-rounds", 30, "jvq rounds per side at most, while undecided")
		benchtime = flag.String("benchtime", "20ms", "go benchmark time per round")
		work      = flag.Int("work", 20000, "base work per op")
		out       = flag.String("out", "test/results", "where to write accuracy.json and accuracy.md")
		jvqPath   = flag.String("jvq", "", "jvq binary (default: build ./cmd/jvq)")
		noiseRuns = flag.Int("noise", 40, "runs of the fixture benchmark alone, to measure the machine's run-to-run noise")
		onlyNoise = flag.Bool("only-noise", false, "measure the noise only, write noise.json and stop")
	)
	flag.Parse()
	start := time.Now()

	bin := *jvqPath
	if bin == "" {
		dir, err := os.MkdirTemp("", "jvq-accuracy-bin-")
		check(err)
		defer os.RemoveAll(dir)
		bin = filepath.Join(dir, "jvq")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		b, err := exec.Command("go", "build", "-o", bin, "./cmd/jvq").CombinedOutput()
		if err != nil {
			fail("building jvq: %v\n%s", err, b)
		}
	}
	ver, err := exec.Command(bin, "version").Output()
	check(err)

	if *noiseRuns > 0 {
		nz := measureNoise(*noiseRuns, *work, *benchtime)
		nz.Machine = machine()
		check(os.MkdirAll(*out, 0o755))
		b, err := json.MarshalIndent(nz, "", "  ")
		check(err)
		check(os.WriteFile(filepath.Join(*out, "noise.json"), append(b, '\n'), 0o644))
		fmt.Fprintf(os.Stderr, "noise: %d runs, run-to-run spread %.1f%% (standard deviation of log time), %.1f%% between neighbouring runs\n",
			nz.Runs, nz.SpreadPct, nz.NeighbourPct)
		if *onlyNoise {
			return
		}
	}

	var levelList []float64
	for _, s := range strings.Split(*levels, ",") {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		check(err)
		levelList = append(levelList, f)
	}

	o := output{
		Date:    time.Now().UTC().Format("2006-01-02"),
		JVQ:     strings.TrimSpace(string(ver)),
		Machine: machine(),
		Settings: map[string]any{
			"rounds": *rounds, "max_rounds": *maxRounds, "benchtime": *benchtime, "work": *work,
			"budget": "5% time (the default)", "confidence": 0.95,
			"workload": "a CPU-bound xorshift loop, about 35 µs per op on the recording machine",
		},
	}

	timeCfg := fixture.Config(*rounds, *benchtime, fmt.Sprintf("max_rounds = %d\n", *maxRounds))
	allocCfg := fixture.Config(3, "10ms", "[budget]\ntime = \"off\"\nallocs = \"0\"\n")

	// A/A: one repository, reused; head only changes a comment.
	{
		r := mustRepo(fixture.Spec{Work: *work}, fixture.Spec{Work: *work, Extra: "nothing measured changed"}, timeCfg)
		for i := 0; i < *aa; i++ {
			t := runOnce(bin, r.ModDir(), "aa", 0, "time")
			o.Trials = append(o.Trials, t)
			progress("A/A", i+1, *aa, t)
		}
		os.RemoveAll(r.Dir)
	}
	for _, p := range levelList {
		headWork := int(float64(*work)*(1+p) + 0.5)
		r := mustRepo(fixture.Spec{Work: *work}, fixture.Spec{Work: headWork}, timeCfg)
		name := fmt.Sprintf("A/B +%g%%", p*100)
		for i := 0; i < *ab; i++ {
			t := runOnce(bin, r.ModDir(), "ab", p, "time")
			o.Trials = append(o.Trials, t)
			progress(name, i+1, *ab, t)
		}
		os.RemoveAll(r.Dir)
	}
	if *allocs > 0 {
		r := mustRepo(fixture.Spec{Work: 2000}, fixture.Spec{Work: 2000, Allocs: 1}, allocCfg)
		for i := 0; i < *allocs; i++ {
			t := runOnce(bin, r.ModDir(), "alloc", 0, "allocs")
			o.Trials = append(o.Trials, t)
			progress("allocs +1", i+1, *allocs, t)
		}
		os.RemoveAll(r.Dir)
		r = mustRepo(fixture.Spec{Work: 2000}, fixture.Spec{Work: 2000, Extra: "no change"}, allocCfg)
		for i := 0; i < *allocs; i++ {
			t := runOnce(bin, r.ModDir(), "alloc-aa", 0, "allocs")
			o.Trials = append(o.Trials, t)
			progress("allocs no change", i+1, *allocs, t)
		}
		os.RemoveAll(r.Dir)
	}

	o.Summary = summarise(o.Trials, levelList)
	o.TotalSecs = time.Since(start).Seconds()
	check(os.MkdirAll(*out, 0o755))
	b, err := json.MarshalIndent(o, "", "  ")
	check(err)
	check(os.WriteFile(filepath.Join(*out, "accuracy.json"), append(b, '\n'), 0o644))
	md := markdown(o)
	check(os.WriteFile(filepath.Join(*out, "accuracy.md"), []byte(md), 0o644))
	fmt.Print(md)
}

type noise struct {
	Date         string         `json:"date"`
	Runs         int            `json:"runs"`
	Benchtime    string         `json:"benchtime"`
	Work         int            `json:"work"`
	NsPerOp      []float64      `json:"ns_per_op"`
	MedianNs     float64        `json:"median_ns"`
	SpreadPct    float64        `json:"spread_pct"`    // standard deviation of ln(ns/op), as a percentage
	NeighbourPct float64        `json:"neighbour_pct"` // median absolute change between consecutive runs
	Machine      map[string]any `json:"machine"`
}

// measureNoise builds the fixture benchmark once and runs it n times in a
// row, to show how much one machine's timings move between runs of the very
// same binary. That is the noise jvq has to see through.
func measureNoise(n, work int, benchtime string) noise {
	dir, err := os.MkdirTemp("", "jvq-accuracy-noise-")
	check(err)
	defer os.RemoveAll(dir)
	r, err := fixture.New(dir, "", fixture.Spec{Work: work})
	check(err)
	exe := filepath.Join(r.ModDir(), "bench.test")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	build := exec.Command("go", "test", "-c", "-o", exe, ".")
	build.Dir = r.ModDir()
	if b, err := build.CombinedOutput(); err != nil {
		fail("building the noise benchmark: %v\n%s", err, b)
	}
	nz := noise{Date: time.Now().UTC().Format("2006-01-02"), Runs: n, Benchtime: benchtime, Work: work}
	var logs []float64
	for i := 0; i < n; i++ {
		out, err := exec.Command(exe, "-test.run=NONE", "-test.bench=.", "-test.benchtime="+benchtime).Output()
		check(err)
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) >= 4 && strings.HasPrefix(f[0], "BenchmarkWork") && f[3] == "ns/op" {
				v, err := strconv.ParseFloat(f[2], 64)
				check(err)
				nz.NsPerOp = append(nz.NsPerOp, v)
				logs = append(logs, math.Log(v))
			}
		}
	}
	if len(logs) < 2 {
		fail("the noise benchmark printed no results")
	}
	mean := 0.0
	for _, l := range logs {
		mean += l
	}
	mean /= float64(len(logs))
	ss := 0.0
	for _, l := range logs {
		ss += (l - mean) * (l - mean)
	}
	nz.SpreadPct = 100 * math.Sqrt(ss/float64(len(logs)-1))
	var steps []float64
	for i := 1; i < len(logs); i++ {
		steps = append(steps, math.Abs(logs[i]-logs[i-1]))
	}
	sort.Float64s(steps)
	nz.NeighbourPct = 100 * steps[len(steps)/2]
	sorted := append([]float64(nil), nz.NsPerOp...)
	sort.Float64s(sorted)
	nz.MedianNs = sorted[len(sorted)/2]
	return nz
}

func mustRepo(base, head fixture.Spec, cfg string) *fixture.Repo {
	dir, err := os.MkdirTemp("", "jvq-accuracy-repo-")
	check(err)
	r, err := fixture.New(dir, "", base)
	check(err)
	_, err = r.Commit(head, "head")
	check(err)
	check(r.WriteConfig(cfg))
	return r
}

func runOnce(bin, dir, kind string, level float64, metric string) trial {
	jsonPath := filepath.Join(os.TempDir(), fmt.Sprintf("jvq-accuracy-%d.json", os.Getpid()))
	os.Remove(jsonPath)
	start := time.Now()
	cmd := exec.Command(bin, "run", "-q", "-json", jsonPath)
	cmd.Dir = dir
	cmd.Env = cleanEnv()
	outb, err := cmd.CombinedOutput()
	t := trial{Kind: kind, Level: level, Seconds: time.Since(start).Seconds()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		t.Exit = ee.ExitCode()
	} else if err != nil {
		fail("running jvq: %v", err)
	}
	if t.Exit == 2 {
		fail("jvq failed:\n%s", outb)
	}
	b, err := os.ReadFile(jsonPath)
	check(err)
	var rep struct {
		Rounds  int `json:"rounds"`
		Results []struct {
			Name    string   `json:"name"`
			Metric  string   `json:"metric"`
			Verdict string   `json:"verdict"`
			Change  *float64 `json:"change"`
			Lo      *float64 `json:"ci_lo"`
			Hi      *float64 `json:"ci_hi"`
		} `json:"results"`
	}
	check(json.Unmarshal(b, &rep))
	t.Rounds = rep.Rounds
	for _, r := range rep.Results {
		if r.Name == "BenchmarkWork" && r.Metric == metric {
			t.Verdict, t.Change, t.Lo, t.Hi = r.Verdict, r.Change, r.Lo, r.Hi
		}
	}
	if t.Verdict == "" {
		fail("no BenchmarkWork %s row in %s", metric, b)
	}
	return t
}

func progress(name string, i, n int, t trial) {
	c := ""
	if t.Change != nil {
		c = fmt.Sprintf(" %+.1f%%", *t.Change*100)
	}
	fmt.Fprintf(os.Stderr, "%s %d/%d: %s%s, %d rounds (%.1fs)\n", name, i, n, t.Verdict, c, t.Rounds, t.Seconds)
}

func summarise(trials []trial, levels []float64) []summaryRow {
	var rows []summaryRow
	add := func(test, injected, expected string, keep func(trial) bool) {
		var r summaryRow
		var changes []float64
		var rounds []int
		for _, t := range trials {
			if !keep(t) {
				continue
			}
			r.Runs++
			rounds = append(rounds, t.Rounds)
			switch t.Verdict {
			case "regression":
				r.Regression++
			case "inconclusive":
				r.Inconcl++
			default:
				r.Pass++
			}
			if t.Change != nil {
				changes = append(changes, *t.Change)
			}
		}
		if r.Runs == 0 {
			return
		}
		sort.Float64s(changes)
		if len(changes) > 0 {
			r.MedChange = changes[len(changes)/2]
		}
		sort.Ints(rounds)
		r.MedRounds = rounds[len(rounds)/2]
		r.Test, r.Injected, r.Expected = test, injected, expected
		rows = append(rows, r)
	}
	add("no change (A/A)", "0%", "no regression", func(t trial) bool { return t.Kind == "aa" })
	for _, p := range levels {
		p := p
		exp := "regression"
		if p <= 0.05 {
			exp = "no regression (within the 5% budget)"
		}
		add("slowdown (A/B)", fmt.Sprintf("+%g%%", p*100), exp, func(t trial) bool { return t.Kind == "ab" && t.Level == p })
	}
	add("allocations", "+1 alloc/op", "regression", func(t trial) bool { return t.Kind == "alloc" })
	add("allocations, no change", "0", "no regression", func(t trial) bool { return t.Kind == "alloc-aa" })
	return rows
}

func pct(n, d int) string {
	if d == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(d))
}

func markdown(o output) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# jvq accuracy\n\nRecorded %s with %s on %s, %v CPUs (%s). ", o.Date, o.JVQ, o.Machine["cpu"], o.Machine["cpus"], o.Machine["os"])
	fmt.Fprintf(&b, "Each run is a full `jvq run` with the default settings: two git worktrees, a build per side, a warm-up, then %v interleaved rounds per side of a %s Go benchmark, adding rounds up to %v while undecided, judged against the 5%% time budget at 95%% confidence. Total time %s.\n\n",
		o.Settings["rounds"], o.Settings["benchtime"], o.Settings["max_rounds"], (time.Duration(o.TotalSecs) * time.Second).String())
	fmt.Fprintln(&b, "| Test | Injected | Runs | Regression | Pass | Inconclusive | Median measured change | Median rounds | Right answer |")
	fmt.Fprintln(&b, "|---|---|---:|---:|---:|---:|---:|---:|---|")
	for _, r := range o.Summary {
		med := fmt.Sprintf("%+.1f%%", r.MedChange*100)
		if strings.HasPrefix(r.Test, "allocations") {
			med = "-"
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %d (%s) | %d | %d | %s | %d | %s |\n", r.Test, r.Injected, r.Runs,
			r.Regression, pct(r.Regression, r.Runs), r.Pass, r.Inconcl, med, r.MedRounds, r.Expected)
	}
	fmt.Fprintln(&b, "\n\"Injected\" is the extra work per op in the fixture benchmark (a CPU loop whose length is set by a constant). Regression means jvq exited 1; pass and inconclusive both exit 0 by default.")
	return b.String()
}

func machine() map[string]any {
	m := map[string]any{
		"os":   runtime.GOOS + "/" + runtime.GOARCH,
		"cpus": runtime.NumCPU(),
		"go":   goVersion(),
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				m["cpu"] = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				break
			}
		}
	}
	if _, ok := m["cpu"]; !ok {
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			m["cpu"] = strings.TrimSpace(string(out))
		} else {
			m["cpu"] = "unknown CPU"
		}
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		m["runner"] = "GitHub-hosted " + os.Getenv("RUNNER_OS") + " runner"
	} else if h := os.Getenv("JVQ_MACHINE"); h != "" {
		m["runner"] = h
	}
	return m
}

func goVersion() string {
	out, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		return runtime.Version()
	}
	return strings.TrimSpace(string(out))
}

func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GITHUB_") {
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
	fmt.Fprintf(os.Stderr, "accuracy: "+format+"\n", a...)
	os.Exit(1)
}
