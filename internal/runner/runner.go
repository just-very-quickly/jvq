// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package runner runs the benchmark command on base and head in alternating
// rounds and collects the samples.
//
// The order is ABBA ABBA...: round 1 runs base then head, round 2 head then
// base, and so on. A machine that slowly gets faster or slower during the run
// (thermal throttling, a neighbour starting up, caches warming) then hits
// both sides about equally, instead of all of it landing on whichever side
// runs second. One warm-up round per side runs first and is thrown away.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/just-very-quickly/jvq/internal/adapter"
	"github.com/just-very-quickly/jvq/internal/stats"
)

// Side is one of the two checkouts.
type Side struct {
	Name string // "base" or "head"
	Dir  string // the worktree root
}

// Options says what to run and how often.
type Options struct {
	Command string
	Build   string // runs once per side before the rounds, if set
	Adapter string
	// Output is a file, relative to where the command runs, that the
	// command writes its results to. It's removed before each run and read
	// after it, instead of the command's standard output. "" reads stdout.
	Output string
	Rounds int // rounds per side to start with
	// MaxRounds caps the rounds per side when More asks for more. At or
	// below Rounds, no extra rounds run.
	MaxRounds int
	// More is asked after the first Rounds and after every extra pair of
	// rounds; while it returns true and MaxRounds allows, two more rounds
	// run (one ABBA block, so the order stays balanced). Nil means never.
	More   func(*Result) bool
	Warmup bool
	// Size lists files, as glob patterns relative to where the command
	// runs, whose sizes are measured once per side after the build.
	Size     []string
	Timeout  time.Duration // per command run; 0 means none
	Subdir   string        // where to run, relative to the worktree root
	Progress io.Writer     // progress lines; nil for none
	// Sequential runs all of base, then all of head. It exists only so the
	// tests can show what interleaving buys; the CLI doesn't offer it.
	Sequential bool
}

// Result holds every sample, by side.
type Result struct {
	Base, Head []adapter.Sample
	// Order lists the measured runs as they happened, such as
	// ["base", "head", "head", "base", ...]; warm-up runs aren't in it.
	Order []string
	// Wall is the wall-clock time of each measured run, by side.
	Wall map[string][]time.Duration
	// Rounds is how many rounds per side ran, extra ones included.
	Rounds int
}

// StepError is a build or command run that failed.
type StepError struct {
	Side   string // "base" or "head"
	Stage  string // "build" or "run"
	Output string // the end of its combined output
	Err    error
}

func (e *StepError) Error() string {
	return fmt.Sprintf("%s %s failed: %v", e.Side, e.Stage, e.Err)
}

func (e *StepError) Unwrap() error { return e.Err }

// ErrNoResults means a run's output held no benchmark results.
var ErrNoResults = errors.New("the output has no benchmark results")

// tailBytes is how much output is kept for error messages.
const tailBytes = 8 << 10

// Plan returns the measured run order for n rounds.
func Plan(rounds int, sequential bool) []int {
	order := make([]int, 0, 2*rounds)
	if sequential {
		for i := 0; i < rounds; i++ {
			order = append(order, 0)
		}
		for i := 0; i < rounds; i++ {
			order = append(order, 1)
		}
		return order
	}
	for r := 0; r < rounds; r++ {
		if r%2 == 0 {
			order = append(order, 0, 1)
		} else {
			order = append(order, 1, 0)
		}
	}
	return order
}

// Run builds both sides (if a build command is set), runs the warm-up round,
// then the measured rounds.
func Run(ctx context.Context, opt Options, base, head Side) (*Result, error) {
	if opt.Rounds < 1 {
		return nil, errors.New("rounds must be at least 1")
	}
	if adapter.Counted(opt.Adapter) {
		if err := valgrindReady(); err != nil {
			return nil, err
		}
	}
	sides := [2]Side{base, head}
	res := &Result{Wall: map[string][]time.Duration{}}
	progress := func(format string, a ...any) {
		if opt.Progress != nil {
			fmt.Fprintf(opt.Progress, format+"\n", a...)
		}
	}
	if opt.Build != "" {
		for _, s := range sides {
			progress("jvq: building %s", s.Name)
			start := time.Now()
			if _, _, err := step(ctx, opt, s, "build", opt.Build, 0); err != nil {
				return nil, err
			}
			progress("jvq: built %s in %s", s.Name, time.Since(start).Round(time.Millisecond))
		}
	}
	if len(opt.Size) > 0 {
		b, bhit, err := sizes(opt, sides[0])
		if err != nil {
			return nil, err
		}
		h, hhit, err := sizes(opt, sides[1])
		if err != nil {
			return nil, err
		}
		for _, p := range opt.Size {
			if !bhit[p] && !hhit[p] {
				return nil, fmt.Errorf("size: %q matches no file on base or head (after the build, in the folder the command runs in; a folder's contents aren't added up, so name the files)", p)
			}
		}
		res.Base = append(res.Base, b...)
		res.Head = append(res.Head, h...)
		progress("jvq: measured %d file size(s) on base and %d on head", len(b), len(h))
	}
	if opt.Warmup && !adapter.Counted(opt.Adapter) { // a count gains nothing from warming up
		for _, s := range sides {
			progress("jvq: warm-up run on %s", s.Name)
			out, elapsed, err := step(ctx, opt, s, "run", opt.Command, 0)
			if err != nil {
				return nil, err
			}
			if _, err := parse(opt, s, out, elapsed); err != nil {
				return nil, err
			}
		}
	}
	measure := func(order []int, first int) error {
		for i, which := range order {
			s := sides[which]
			round := first + i/2
			if opt.Sequential {
				round = first + i%opt.Rounds
			}
			out, wall, err := step(ctx, opt, s, "run", opt.Command, round)
			if err != nil {
				return err
			}
			samples, err := parse(opt, s, out, wall)
			if err != nil {
				return err
			}
			samples = PerRun(samples)
			total := opt.Rounds
			if res.Rounds+1 > total {
				total = opt.MaxRounds
			}
			progress("jvq: round %d/%d %s %s (%d results)", round, total, s.Name, wall.Round(time.Millisecond), len(samples))
			res.Order = append(res.Order, s.Name)
			res.Wall[s.Name] = append(res.Wall[s.Name], wall)
			if which == 0 {
				res.Base = append(res.Base, samples...)
			} else {
				res.Head = append(res.Head, samples...)
			}
		}
		return nil
	}
	if err := measure(Plan(opt.Rounds, opt.Sequential), 1); err != nil {
		return nil, err
	}
	res.Rounds = opt.Rounds
	for opt.More != nil && res.Rounds+2 <= opt.MaxRounds && opt.More(res) {
		if res.Rounds == opt.Rounds {
			progress("jvq: undecided after %d rounds; adding rounds, up to %d", opt.Rounds, opt.MaxRounds)
		}
		// Two more rounds continue the alternation: the next block starts
		// with whichever side the pattern says.
		next := res.Rounds + 1
		order := []int{0, 1, 1, 0}
		if next%2 == 0 {
			order = []int{1, 0, 0, 1}
		}
		if err := measure(order, next); err != nil {
			return nil, err
		}
		res.Rounds += 2
	}
	return res, nil
}

// sizes measures the files the Size patterns match on one side. A file two
// patterns match is measured once. hit says which patterns matched a file.
func sizes(opt Options, s Side) (out []adapter.Sample, hit map[string]bool, err error) {
	dir := s.Dir
	if opt.Subdir != "" && opt.Subdir != "." {
		dir = filepath.Join(s.Dir, opt.Subdir)
	}
	seen := map[string]bool{}
	hit = map[string]bool{}
	for _, p := range opt.Size {
		// Globbing inside the folder keeps its own path out of the
		// pattern, and keeps the pattern inside the worktree.
		matches, err := fs.Glob(os.DirFS(dir), p)
		if err != nil {
			return nil, nil, fmt.Errorf("size pattern %q: %w", p, err)
		}
		sort.Strings(matches)
		for _, m := range matches {
			fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(m)))
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			name := m
			hit[p] = true
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, adapter.Sample{Name: name, Metric: adapter.Size, Unit: "B", Value: float64(fi.Size())})
		}
	}
	return out, hit, nil
}

// PerRun reduces one run's samples to one per benchmark and metric: the
// median of what the run printed for it. Values from one run share that
// run's state of the machine (its caches, its neighbours, its clock speed),
// so they aren't independent of each other; judging them as if they were
// would make the intervals too narrow. Each run is one observation.
func PerRun(samples []adapter.Sample) []adapter.Sample {
	type key struct{ k, metric, unit string }
	order := []key{}
	values := map[key][]float64{}
	first := map[key]adapter.Sample{}
	for _, x := range samples {
		k := key{x.Key(), x.Metric, x.Unit}
		if _, ok := values[k]; !ok {
			order = append(order, k)
			first[k] = x
		}
		values[k] = append(values[k], x.Value)
	}
	if len(order) == len(samples) {
		return samples
	}
	out := make([]adapter.Sample, 0, len(order))
	for _, k := range order {
		x := first[k]
		x.Value = stats.Median(values[k])
		out = append(out, x)
	}
	return out
}

func parse(opt Options, s Side, out []byte, elapsed time.Duration) ([]adapter.Sample, error) {
	if adapter.Timed(opt.Adapter) {
		return []adapter.Sample{adapter.TimeSample(elapsed)}, nil
	}
	if adapter.Counted(opt.Adapter) {
		n, err := strconv.ParseFloat(string(out), 64)
		if err != nil || n <= 0 {
			return nil, &StepError{Side: s.Name, Stage: "run", Err: fmt.Errorf("valgrind counted no instructions")}
		}
		return []adapter.Sample{adapter.InstructionSample(n)}, nil
	}
	samples, err := adapter.Parse(opt.Adapter, bytes.NewReader(out))
	if err != nil {
		return nil, &StepError{Side: s.Name, Stage: "run", Output: tail(out), Err: err}
	}
	if len(samples) == 0 {
		return nil, &StepError{Side: s.Name, Stage: "run", Output: tail(out), Err: fmt.Errorf("%w; %s", ErrNoResults, hint(opt))}
	}
	return samples, nil
}

// hint says what the adapter looks for, for an output with no results.
func hint(opt Options) string {
	where := "its standard output"
	if opt.Output != "" {
		where = opt.Output
	}
	switch opt.Adapter {
	case "json":
		return "the json adapter reads results such as {\"name\": \"parse\", \"unit\": \"ns\", \"value\": 912} from " + where
	case "criterion":
		return "the criterion adapter reads cargo bench's time: [...] lines from " + where
	case "google-benchmark":
		return "the google-benchmark adapter reads --benchmark_format=json output from " + where
	case "pytest-benchmark":
		return "the pytest-benchmark adapter reads the file --benchmark-json writes; set output to it"
	case "vitest":
		return "the vitest adapter reads the file vitest bench --outputJson writes; set output to it"
	}
	return "the go adapter reads `go test -bench` output from " + where + "; check the command prints Benchmark lines"
}

func tail(b []byte) string {
	if len(b) > tailBytes {
		b = b[len(b)-tailBytes:]
	}
	return string(b)
}

// step runs one shell command in a side's worktree and returns its results
// (its stdout, or the Output file for a run) and how long it took. Only the
// results are parsed, but an error message shows stdout and stderr both.
func step(ctx context.Context, opt Options, s Side, stage, command string, round int) ([]byte, time.Duration, error) {
	if opt.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.Timeout)
		defer cancel()
	}
	dir := s.Dir
	if opt.Subdir != "" && opt.Subdir != "." {
		dir = filepath.Join(s.Dir, opt.Subdir)
	}
	counted := stage == "run" && adapter.Counted(opt.Adapter)
	var logDir string
	if counted {
		d, err := os.MkdirTemp("", "jvq-valgrind-*")
		if err != nil {
			return nil, 0, &StepError{Side: s.Name, Stage: stage, Err: err}
		}
		defer os.RemoveAll(d)
		logDir = d
		command = valgrindCommand(command, d)
	}
	cmd := shell(ctx, command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "JVQ_SIDE="+s.Name, "JVQ_ROUND="+strconv.Itoa(round))
	if counted && os.Getenv("GOMAXPROCS") == "" {
		// Valgrind runs one thread at a time, and Go's idle threads spin
		// while they wait for work: with more than one P that spinning is
		// counted, and it varies by 20% or more from run to run. One P
		// keeps a Go program's count steady and other programs ignore it.
		cmd.Env = append(cmd.Env, "GOMAXPROCS=1")
	}
	// stdout and stderr are copied by two goroutines, so the buffer they
	// share is locked.
	var stdout bytes.Buffer
	combined := &lockedBuffer{}
	timed := stage == "run" && adapter.Measured(opt.Adapter)
	var logFile *os.File
	if timed {
		// The time adapter ignores the output, and its clock must stop
		// when the command exits, not when the last background process it
		// started lets go of a pipe. A file needs no copying goroutines,
		// so Wait returns at the exit.
		f, err := os.CreateTemp("", "jvq-output-*")
		if err != nil {
			return nil, 0, &StepError{Side: s.Name, Stage: stage, Err: err}
		}
		defer os.Remove(f.Name())
		defer f.Close()
		logFile = f
		cmd.Stdout, cmd.Stderr = f, f
	} else {
		cmd.Stdout = io.MultiWriter(&stdout, combined)
		cmd.Stderr = combined
		cmd.WaitDelay = 5 * time.Second
	}
	output := func() string {
		if logFile != nil {
			return fileTail(logFile)
		}
		return tail(combined.Bytes())
	}
	var outFile string
	if opt.Output != "" && stage == "run" {
		outFile = opt.Output
		if !filepath.IsAbs(outFile) {
			outFile = filepath.Join(dir, outFile)
		}
		// A file left by an earlier run must not pass for this run's.
		if fi, err := os.Lstat(outFile); err == nil {
			if !fi.Mode().IsRegular() {
				return nil, 0, &StepError{Side: s.Name, Stage: stage, Err: fmt.Errorf("the output path %s isn't a regular file", opt.Output)}
			}
			if err := os.Remove(outFile); err != nil {
				return nil, 0, &StepError{Side: s.Name, Stage: stage, Err: fmt.Errorf("removing the old output file: %w", err)}
			}
		}
	}
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		// The command finished and succeeded, but something it started in
		// the background kept its output open. The results are complete.
		err = nil
	}
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("timed out after %s", opt.Timeout)
		} else if ctx.Err() != nil {
			err = ctx.Err()
		}
		out := output()
		if counted {
			out = joinOutput(out, valgrindTail(logDir))
		}
		return nil, 0, &StepError{Side: s.Name, Stage: stage, Output: out, Err: err}
	}
	if counted {
		n, err := sumInstructions(logDir)
		if err != nil {
			return nil, 0, &StepError{Side: s.Name, Stage: stage, Output: joinOutput(output(), valgrindTail(logDir)), Err: err}
		}
		return []byte(strconv.FormatFloat(n, 'f', -1, 64)), elapsed, nil
	}
	if outFile != "" {
		b, err := os.ReadFile(outFile)
		if err != nil {
			return nil, 0, &StepError{Side: s.Name, Stage: stage, Output: output(),
				Err: fmt.Errorf("the command didn't write its output file: %w", err)}
		}
		return b, elapsed, nil
	}
	return stdout.Bytes(), elapsed, nil
}

// valgrindReady checks the instructions adapter can run here.
func valgrindReady() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("the instructions adapter runs the command under valgrind, which jvq supports on Linux only (this is %s)", runtime.GOOS)
	}
	if _, err := exec.LookPath("valgrind"); err != nil {
		return errors.New("the instructions adapter needs valgrind on the PATH (apt install valgrind, or your system's package)")
	}
	return nil
}

// valgrindCommand wraps a shell command so valgrind's cachegrind counts
// every instruction it and the processes it starts run, writing one log per
// process into logDir. The cache simulation is off: only the count is used.
func valgrindCommand(command, logDir string) string {
	return "valgrind --tool=cachegrind --cache-sim=no --cachegrind-out-file=/dev/null --trace-children=yes" +
		" --log-file=" + shQuote(filepath.Join(logDir, "vg.%p")) + " sh -c " + shQuote(command)
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// instrLine matches the count in a cachegrind summary, such as
// "==2471== I refs:        13,438,495" (or "I   refs" in older versions).
var instrLine = regexp.MustCompile(`\bI\s+refs:\s+([0-9,]+)`)

// sumInstructions adds up the counts in the logs valgrind wrote, one per
// process the command ran.
func sumInstructions(logDir string) (float64, error) {
	files, err := filepath.Glob(filepath.Join(logDir, "vg.*"))
	if err != nil || len(files) == 0 {
		return 0, errors.New("valgrind wrote no log")
	}
	total, found := 0.0, false
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return 0, err
		}
		for _, m := range instrLine.FindAllSubmatch(b, -1) {
			n, err := strconv.ParseFloat(strings.ReplaceAll(string(m[1]), ",", ""), 64)
			if err == nil {
				total += n
				found = true
			}
		}
	}
	if !found {
		return 0, errors.New("valgrind's logs have no instruction count (I refs)")
	}
	return total, nil
}

// valgrindTail is the end of valgrind's own logs, where its errors go
// (an instruction it can't run, a tool that didn't start), so they can be
// shown before the folder is removed.
func valgrindTail(logDir string) string {
	files, _ := filepath.Glob(filepath.Join(logDir, "vg.*"))
	var b bytes.Buffer
	for _, f := range files {
		if data, err := os.ReadFile(f); err == nil {
			b.Write(data)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "valgrind's log:\n" + tail(b.Bytes())
}

func joinOutput(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n" + b
}

// fileTail is the end of what a command wrote to f.
func fileTail(f *os.File) string {
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	off := fi.Size() - tailBytes
	if off < 0 {
		off = 0
	}
	b := make([]byte, fi.Size()-off)
	n, _ := f.ReadAt(b, off)
	return string(b[:n])
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Bytes()
}
