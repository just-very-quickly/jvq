// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Command scenarios records how jvq holds up in three harder settings, each
// with a known right answer, and writes test/results/scenarios.json and
// scenarios.md. scripts/record.sh runs it after the accuracy suite.
//
//   - Noisy neighbour: the Go fixture benchmark, compared by the real jvq
//     binary while another process keeps half the CPUs busy and churns
//     through memory. No change, and +10% more work.
//
//   - Drift: a machine that gets slower with every run, the same for base
//     and head (the fixture program's JVQ_FIXTURE_DRIFT). No change, judged
//     once with jvq's alternating ABBA order and once with all of base run
//     before all of head. This drives the runner and the verdict code
//     directly, since the CLI has no sequential order.
//
//   - Commands that aren't Go: a Python script with a work loop, timed whole
//     by the time adapter, and the same script reporting its own batches
//     with the json adapter.
//
//     go run ./test/scenarios -out test/results
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/just-very-quickly/jvq/internal/config"
	"github.com/just-very-quickly/jvq/internal/fixture"
	"github.com/just-very-quickly/jvq/internal/runner"
	"github.com/just-very-quickly/jvq/internal/verdict"
)

type trial struct {
	Scenario string   `json:"scenario"`
	Case     string   `json:"case"`
	Verdict  string   `json:"verdict"`
	Change   *float64 `json:"change,omitempty"`
	Lo       *float64 `json:"ci_lo,omitempty"`
	Hi       *float64 `json:"ci_hi,omitempty"`
	Rounds   int      `json:"rounds"`
	Seconds  float64  `json:"seconds"`
}

type row struct {
	Scenario   string  `json:"scenario"`
	Case       string  `json:"case"`
	Runs       int     `json:"runs"`
	Regression int     `json:"regression"`
	Pass       int     `json:"pass"`
	Inconcl    int     `json:"inconclusive"`
	MedChange  float64 `json:"median_change"`
	MedRounds  int     `json:"median_rounds"`
	Expected   string  `json:"expected"`
}

type output struct {
	Date      string         `json:"date"`
	JVQ       string         `json:"jvq"`
	Machine   map[string]any `json:"machine"`
	Settings  map[string]any `json:"settings"`
	Trials    []trial        `json:"trials"`
	Summary   []row          `json:"summary"`
	TotalSecs float64        `json:"total_seconds"`
}

const work = 20000

func main() {
	var (
		hog       = flag.Bool("hog", false, "be the noisy neighbour: spin every CPU and churn memory until killed")
		hogMB     = flag.Int("hog-mb", 512, "memory the neighbour churns through, in MB")
		hogCPUs   = flag.Int("hog-cpus", max(1, runtime.NumCPU()/2), "CPUs the neighbour keeps busy")
		noisyAA   = flag.Int("noisy-aa", 100, "no-change runs beside the neighbour")
		noisyAB   = flag.Int("noisy-ab", 50, "+10% runs beside the neighbour")
		driftRuns = flag.Int("drift", 50, "drift runs for each order")
		driftPct  = flag.Float64("drift-pct", 1, "how much slower each run is than the one before, in percent")
		pyAA      = flag.Int("python-aa", 60, "no-change runs per Python adapter")
		pyAB      = flag.Int("python-ab", 30, "runs per Python slowdown")
		pyWork    = flag.Int("python-work", 800000, "loop rounds in the Python script")
		only      = flag.String("only", "", "run only these scenarios: noisy, drift, python (comma-separated)")
		out       = flag.String("out", "test/results", "where to write scenarios.json and scenarios.md")
		jvqPath   = flag.String("jvq", "", "jvq binary (default: build ./cmd/jvq)")
	)
	flag.Parse()
	if *hog {
		beNeighbour(*hogCPUs, *hogMB)
		return
	}
	start := time.Now()
	want := func(s string) bool { return *only == "" || strings.Contains(","+*only+",", ","+s+",") }

	bin := *jvqPath
	if bin == "" {
		dir, err := os.MkdirTemp("", "jvq-scenarios-bin-")
		check(err)
		defer os.RemoveAll(dir)
		bin = filepath.Join(dir, "jvq"+exeSuffix())
		if b, err := exec.Command("go", "build", "-o", bin, "./cmd/jvq").CombinedOutput(); err != nil {
			fail("building jvq: %v\n%s", err, b)
		}
	}
	ver, err := exec.Command(bin, "version").Output()
	check(err)
	o := output{
		Date: time.Now().UTC().Format("2006-01-02"), JVQ: strings.TrimSpace(string(ver)), Machine: machine(),
		Settings: map[string]any{
			"budget": "5% time (the default)", "confidence": 0.95,
			"noisy":  fmt.Sprintf("the Go fixture benchmark (20 ms a round, 10 rounds to start, up to 30), beside a process keeping %d of the %d CPUs busy and churning through %d MB", *hogCPUs, runtime.NumCPU(), *hogMB),
			"drift":  fmt.Sprintf("the fixture program with the json adapter, each run %g%% slower than the one before, 10 rounds a side, no warm-up, no added rounds", *driftPct),
			"python": fmt.Sprintf("a Python script running a %d-round loop: timed whole by the time adapter, or timing 5 batches of a tenth of it with the json adapter; 10 rounds to start, up to 30", *pyWork),
		},
	}

	if want("noisy") {
		o.Trials = append(o.Trials, noisy(bin, *hogCPUs, *hogMB, *noisyAA, *noisyAB)...)
	}
	if want("drift") {
		o.Trials = append(o.Trials, drift(*driftRuns, *driftPct)...)
	}
	if want("python") {
		if _, err := exec.LookPath(python()); err != nil {
			fail("the python scenario needs %s on the PATH", python())
		}
		o.Trials = append(o.Trials, pythonRuns(bin, *pyWork, *pyAA, *pyAB)...)
	}
	o.Summary = summarise(o.Trials)
	o.TotalSecs = time.Since(start).Seconds()
	check(os.MkdirAll(*out, 0o755))
	b, err := json.MarshalIndent(o, "", "  ")
	check(err)
	check(os.WriteFile(filepath.Join(*out, "scenarios.json"), append(b, '\n'), 0o644))
	md := markdown(o)
	check(os.WriteFile(filepath.Join(*out, "scenarios.md"), []byte(md), 0o644))
	fmt.Print(md)
}

// beNeighbour keeps cpus CPUs busy and walks a large buffer, until killed.
func beNeighbour(cpus, mb int) {
	buf := make([]byte, mb<<20)
	for i := 0; i < cpus; i++ {
		go func(i int) {
			x := uint64(i + 1)
			for {
				x ^= x << 13
				x ^= x >> 7
				x ^= x << 17
				if i == 0 {
					// One spinner also writes across the whole buffer,
					// competing for caches and memory bandwidth.
					for j := 0; j < len(buf); j += 64 {
						buf[j] = byte(x)
					}
				}
			}
		}(i)
	}
	// Stop with the parent, even if it dies without killing this process.
	parent := os.Getppid()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	for {
		select {
		case <-ch:
			return
		case <-time.After(time.Second):
			if os.Getppid() != parent {
				return
			}
		}
	}
}

func noisy(bin string, cpus, mb, aa, ab int) []trial {
	self, err := os.Executable()
	check(err)
	hog := exec.Command(self, "-hog", "-hog-cpus", fmt.Sprint(cpus), "-hog-mb", fmt.Sprint(mb))
	check(hog.Start())
	defer func() {
		hog.Process.Kill()
		hog.Wait()
	}()
	time.Sleep(time.Second) // let it get going
	cfg := fixture.Config(10, "20ms", "max_rounds = 30\n")
	var trials []trial
	r := mustRepo(fixture.Spec{Work: work}, fixture.Spec{Work: work, Extra: "nothing measured changed"}, cfg)
	for i := 0; i < aa; i++ {
		t := cli(bin, r.ModDir(), "noisy", "no change", "BenchmarkWork")
		trials = append(trials, t)
		progress(t, i+1, aa)
	}
	os.RemoveAll(r.Dir)
	r = mustRepo(fixture.Spec{Work: work}, fixture.Spec{Work: work * 11 / 10}, cfg)
	for i := 0; i < ab; i++ {
		t := cli(bin, r.ModDir(), "noisy", "+10% work", "BenchmarkWork")
		trials = append(trials, t)
		progress(t, i+1, ab)
	}
	os.RemoveAll(r.Dir)
	return trials
}

// drift builds the fixture program for base and head (the same work) and
// runs them with the runner directly, in ABBA order and in sequence, while
// every run is slower than the last.
func drift(n int, pct float64) []trial {
	dir, err := os.MkdirTemp("", "jvq-scenarios-drift-")
	check(err)
	defer os.RemoveAll(dir)
	sides := [2]runner.Side{{Name: "base", Dir: filepath.Join(dir, "base")}, {Name: "head", Dir: filepath.Join(dir, "head")}}
	for i, s := range sides {
		check(os.MkdirAll(s.Dir, 0o755))
		r, err := fixture.New(s.Dir, "", fixture.Spec{Work: work, Extra: fmt.Sprint("side ", i)})
		check(err)
		b := exec.Command("go", "build", "-o", "work"+exeSuffix(), "./prog")
		b.Dir = r.ModDir()
		if out, err := b.CombinedOutput(); err != nil {
			fail("building the fixture program: %v\n%s", err, out)
		}
	}
	counter := filepath.Join(dir, "runs")
	cfg := config.Default()
	cfg.Adapter = "json"
	var trials []trial
	for _, order := range []struct {
		name       string
		sequential bool
	}{{"ABBA order", false}, {"sequential order", true}} {
		for i := 0; i < n; i++ {
			check(os.WriteFile(counter, []byte("0"), 0o644))
			os.Setenv("JVQ_FIXTURE_DRIFT", fmt.Sprintf("%s:%g", counter, pct))
			start := time.Now()
			res, err := runner.Run(context.Background(), runner.Options{
				Command: fixture.ProgExe() + " -json -reps 200", Adapter: "json",
				Rounds: 10, MaxRounds: 10, Timeout: time.Minute, Sequential: order.sequential,
			}, sides[0], sides[1])
			check(err)
			t := fromRows("drift", order.name, verdict.Compare(res.Base, res.Head, cfg), "work", res.Rounds, time.Since(start))
			trials = append(trials, t)
			progress(t, i+1, n)
		}
	}
	os.Unsetenv("JVQ_FIXTURE_DRIFT")
	return trials
}

func python() string {
	if runtime.GOOS == "windows" {
		return "python"
	}
	return "python3"
}

const pyScript = `# %s
import sys
import time

WORK = %d


def op(n):
    x = 88172645463325252
    m = (1 << 64) - 1
    for _ in range(n):
        x ^= (x << 13) & m
        x ^= x >> 7
        x ^= (x << 17) & m
    return x


if "--json" in sys.argv:
    for _ in range(5):
        start = time.perf_counter_ns()
        op(WORK // 10)
        ns = time.perf_counter_ns() - start
        print('{"name": "work", "unit": "ns", "value": %%d}' %% ns)
else:
    op(WORK)
`

func pyRepo(baseWork, headWork int, adapterName string) string {
	dir, err := os.MkdirTemp("", "jvq-scenarios-python-")
	check(err)
	if _, err := fixture.Git(dir, "init", "-q", "-b", "main"); err != nil {
		fail("%v", err)
	}
	cmd := python() + " bench.py"
	if adapterName == "json" {
		cmd += " --json"
	}
	for i, w := range []int{baseWork, headWork} {
		check(os.WriteFile(filepath.Join(dir, "bench.py"), []byte(fmt.Sprintf(pyScript, fmt.Sprint("commit ", i), w)), 0o644))
		if _, err := fixture.Git(dir, "add", "-A"); err != nil {
			fail("%v", err)
		}
		if _, err := fixture.Git(dir, "commit", "-q", "-m", fmt.Sprint("commit ", i)); err != nil {
			fail("%v", err)
		}
	}
	check(os.WriteFile(filepath.Join(dir, "jvq.toml"), []byte(fmt.Sprintf("adapter = %q\ncommand = %q\n", adapterName, cmd)), 0o644))
	return dir
}

func pythonRuns(bin string, w, aa, ab int) []trial {
	var trials []trial
	for _, a := range []string{"time", "json"} {
		name := map[string]string{"time": "command", "json": "work"}[a]
		scenario := "python, " + a + " adapter"
		cases := []struct {
			label string
			head  int
			runs  int
		}{{"no change", w, aa}, {"+10% work", w * 11 / 10, ab}, {"+20% work", w * 12 / 10, ab}}
		for _, c := range cases {
			dir := pyRepo(w, c.head, a)
			for i := 0; i < c.runs; i++ {
				t := cli(bin, dir, scenario, c.label, name)
				trials = append(trials, t)
				progress(t, i+1, c.runs)
			}
			os.RemoveAll(dir)
		}
	}
	return trials
}

func mustRepo(base, head fixture.Spec, cfg string) *fixture.Repo {
	dir, err := os.MkdirTemp("", "jvq-scenarios-repo-")
	check(err)
	r, err := fixture.New(dir, "", base)
	check(err)
	_, err = r.Commit(head, "head")
	check(err)
	check(r.WriteConfig(cfg))
	return r
}

// cli runs jvq once and reads the time row for the named benchmark.
func cli(bin, dir, scenario, label, name string) trial {
	jsonPath := filepath.Join(os.TempDir(), fmt.Sprintf("jvq-scenarios-%d.json", os.Getpid()))
	os.Remove(jsonPath)
	start := time.Now()
	cmd := exec.Command(bin, "run", "-q", "-json", jsonPath)
	cmd.Dir = dir
	cmd.Env = cleanEnv()
	outb, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ee.ExitCode() == 2 {
			fail("jvq failed:\n%s", outb)
		}
	} else if err != nil {
		fail("running jvq: %v", err)
	}
	b, err := os.ReadFile(jsonPath)
	check(err)
	var rep struct {
		Rounds  int           `json:"rounds"`
		Results []verdict.Row `json:"results"`
	}
	check(json.Unmarshal(b, &rep))
	return fromRows(scenario, label, rep.Results, name, rep.Rounds, time.Since(start))
}

func fromRows(scenario, label string, rows []verdict.Row, name string, rounds int, d time.Duration) trial {
	t := trial{Scenario: scenario, Case: label, Rounds: rounds, Seconds: d.Seconds()}
	for _, r := range rows {
		if r.Name == name && r.Metric == "time" {
			t.Verdict, t.Change, t.Lo, t.Hi = r.Verdict, r.Change, r.Lo, r.Hi
		}
	}
	if t.Verdict == "" {
		fail("no %s time row in %+v", name, rows)
	}
	return t
}

func progress(t trial, i, n int) {
	c := ""
	if t.Change != nil {
		c = fmt.Sprintf(" %+.1f%%", *t.Change*100)
	}
	fmt.Fprintf(os.Stderr, "%s, %s %d/%d: %s%s, %d rounds (%.1fs)\n", t.Scenario, t.Case, i, n, t.Verdict, c, t.Rounds, t.Seconds)
}

func summarise(trials []trial) []row {
	var rows []row
	index := map[string]int{}
	changes := map[string][]float64{}
	rounds := map[string][]int{}
	for _, t := range trials {
		k := t.Scenario + "|" + t.Case
		i, ok := index[k]
		if !ok {
			exp := "regression"
			if t.Case == "no change" || strings.Contains(t.Case, "order") {
				exp = "no regression"
			}
			rows = append(rows, row{Scenario: t.Scenario, Case: t.Case, Expected: exp})
			i = len(rows) - 1
			index[k] = i
		}
		r := &rows[i]
		r.Runs++
		switch t.Verdict {
		case "regression":
			r.Regression++
		case "inconclusive":
			r.Inconcl++
		default:
			r.Pass++
		}
		if t.Change != nil {
			changes[k] = append(changes[k], *t.Change)
		}
		rounds[k] = append(rounds[k], t.Rounds)
	}
	for k, i := range index {
		if c := changes[k]; len(c) > 0 {
			sort.Float64s(c)
			rows[i].MedChange = c[len(c)/2]
		}
		rs := rounds[k]
		sort.Ints(rs)
		rows[i].MedRounds = rs[len(rs)/2]
	}
	return rows
}

func markdown(o output) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# jvq in harder settings\n\nRecorded %s with %s on %s, %v CPUs (%s). Total time %s. The time budget is the default 5%%, at 95%% confidence.\n\n",
		o.Date, o.JVQ, o.Machine["cpu"], o.Machine["cpus"], o.Machine["os"], (time.Duration(o.TotalSecs) * time.Second).String())
	for _, k := range []string{"noisy", "drift", "python"} {
		fmt.Fprintf(&b, "- **%s**: %s.\n", k, o.Settings[k])
	}
	fmt.Fprintln(&b, "\n| Scenario | Head | Runs | Regression | Pass | Inconclusive | Median measured change | Median rounds | Right answer |")
	fmt.Fprintln(&b, "|---|---|---:|---:|---:|---:|---:|---:|---|")
	for _, r := range o.Summary {
		fmt.Fprintf(&b, "| %s | %s | %d | %d (%s) | %d | %d | %+.1f%% | %d | %s |\n", r.Scenario, r.Case, r.Runs,
			r.Regression, pct(r.Regression, r.Runs), r.Pass, r.Inconcl, r.MedChange*100, r.MedRounds, r.Expected)
	}
	fmt.Fprintln(&b, "\nIn the drift rows head is the same as base; only the order of the runs differs. In the Python rows, \"+10% work\" is 10% more rounds of the loop: with the time adapter the whole command also includes Python's start-up, so its measured change is a little smaller.")
	return b.String()
}

func pct(n, d int) string {
	if d == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(d))
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func machine() map[string]any {
	m := map[string]any{"os": runtime.GOOS + "/" + runtime.GOARCH, "cpus": runtime.NumCPU(), "cpu": "unknown CPU"}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				m["cpu"] = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				break
			}
		}
	}
	if out, err := exec.Command(python(), "--version").Output(); err == nil {
		m["python"] = strings.TrimSpace(string(out))
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		m["runner"] = "GitHub-hosted " + os.Getenv("RUNNER_OS") + " runner"
	} else if h := os.Getenv("JVQ_MACHINE"); h != "" {
		m["runner"] = h
	}
	return m
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
	fmt.Fprintf(os.Stderr, "scenarios: "+format+"\n", a...)
	os.Exit(1)
}
