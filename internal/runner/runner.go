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
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/just-very-quickly/jvq/internal/adapter"
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
	Rounds  int // rounds per side to start with
	// MaxRounds caps the rounds per side when More asks for more. At or
	// below Rounds, no extra rounds run.
	MaxRounds int
	// More is asked after the first Rounds and after every extra pair of
	// rounds; while it returns true and MaxRounds allows, two more rounds
	// run (one ABBA block, so the order stays balanced). Nil means never.
	More     func(*Result) bool
	Warmup   bool
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
			if _, err := step(ctx, opt, s, "build", opt.Build, 0); err != nil {
				return nil, err
			}
			progress("jvq: built %s in %s", s.Name, time.Since(start).Round(time.Millisecond))
		}
	}
	if opt.Warmup {
		for _, s := range sides {
			progress("jvq: warm-up run on %s", s.Name)
			out, err := step(ctx, opt, s, "run", opt.Command, 0)
			if err != nil {
				return nil, err
			}
			if _, err := parse(opt, s, out); err != nil {
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
			start := time.Now()
			out, err := step(ctx, opt, s, "run", opt.Command, round)
			if err != nil {
				return err
			}
			wall := time.Since(start)
			samples, err := parse(opt, s, out)
			if err != nil {
				return err
			}
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

func parse(opt Options, s Side, out []byte) ([]adapter.Sample, error) {
	samples, err := adapter.Parse(opt.Adapter, bytes.NewReader(out))
	if err != nil {
		return nil, &StepError{Side: s.Name, Stage: "run", Output: tail(out), Err: err}
	}
	if len(samples) == 0 {
		return nil, &StepError{Side: s.Name, Stage: "run", Output: tail(out), Err: ErrNoResults}
	}
	return samples, nil
}

func tail(b []byte) string {
	if len(b) > tailBytes {
		b = b[len(b)-tailBytes:]
	}
	return string(b)
}

// step runs one shell command in a side's worktree and returns its stdout.
// The output keeps stdout and stderr apart: only stdout is parsed, but the
// error message shows both.
func step(ctx context.Context, opt Options, s Side, stage, command string, round int) ([]byte, error) {
	if opt.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.Timeout)
		defer cancel()
	}
	dir := s.Dir
	if opt.Subdir != "" && opt.Subdir != "." {
		dir = filepath.Join(s.Dir, opt.Subdir)
	}
	cmd := shell(ctx, command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "JVQ_SIDE="+s.Name, "JVQ_ROUND="+strconv.Itoa(round))
	// stdout and stderr are copied by two goroutines, so the buffer they
	// share is locked.
	var stdout bytes.Buffer
	combined := &lockedBuffer{}
	cmd.Stdout = io.MultiWriter(&stdout, combined)
	cmd.Stderr = combined
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("timed out after %s", opt.Timeout)
		} else if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, &StepError{Side: s.Name, Stage: stage, Output: tail(combined.Bytes()), Err: err}
	}
	return stdout.Bytes(), nil
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
