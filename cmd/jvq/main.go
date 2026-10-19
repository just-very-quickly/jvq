// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Command jvq is a performance budget gate for CI. It measures the base
// branch and the change on the same machine, in alternating rounds, and
// fails when the change is slower than its budget allows.
//
//	jvq run [flags] [-- command]
//	jvq compare [flags] OLD NEW
//	jvq check [--config jvq.toml]
//	jvq version
//
// Exit codes: 0 pass, 1 regression, 2 error. docs/reference.md has the
// details.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/just-very-quickly/jvq/internal/config"
	"github.com/just-very-quickly/jvq/internal/gitx"
	"github.com/just-very-quickly/jvq/internal/report"
	"github.com/just-very-quickly/jvq/internal/runner"
	"github.com/just-very-quickly/jvq/internal/verdict"
)

// Exit codes.
const (
	exitPass       = 0
	exitRegression = 1
	exitError      = 2
)

const usage = `jvq: a performance budget gate for CI

Usage:
  jvq run [flags] [-- command]   compare base and head; fail on a regression
  jvq compare [flags] OLD NEW    compare two saved benchmark outputs
  jvq check [--config FILE]      read jvq.toml and show the settings
  jvq version                    print the version

jvq run checks out the base and head commits into temporary worktrees,
runs the benchmark command on each in alternating rounds, and compares
the results with rank statistics. Without a command it uses the one in
jvq.toml, or in a Go module: ` + config.DefaultGoCommand + `

Exit codes: 0 pass (inconclusive passes unless fail_on_inconclusive),
1 regression, 2 error.

Run "jvq run -h" or "jvq compare -h" for their flags. Docs: https://jvq.net
`

func main() {
	os.Exit(cli(os.Args[1:], os.Stdout, os.Stderr))
}

func cli(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitError
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "compare":
		return cmdCompare(args[1:], stdout, stderr)
	case "check":
		return cmdCheck(args[1:], stdout, stderr)
	case "version", "--version", "-version", "-v":
		fmt.Fprintf(stdout, "jvq %s (%s, %s/%s)\n", Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return exitPass
	case "help", "-h", "--help", "-help":
		fmt.Fprint(stdout, usage)
		return exitPass
	}
	fmt.Fprintf(stderr, "jvq: unknown command %q\n\n%s", args[0], usage)
	return exitError
}

type runFlags struct {
	base, head, configPath, build, adapter, output, jsonPath, mdPath, timeout string
	rounds, maxRounds                                                         int
	baseMayFail, failOnInconclusive, keep, quiet, noWarmup                    bool
}

func cmdRun(args []string, stdout, stderr io.Writer) int {
	var f runFlags
	fs := flag.NewFlagSet("jvq run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.base, "base", "", "the commit to compare against (default: where head branched off the base branch; on the base branch itself, head's parent)")
	fs.StringVar(&f.head, "head", "HEAD", "the commit to check")
	fs.StringVar(&f.configPath, "config", "", "config file (default: jvq.toml here or in a parent, up to the repository root)")
	fs.IntVar(&f.rounds, "rounds", 0, "measured rounds per side to start with (default 10)")
	fs.IntVar(&f.maxRounds, "max-rounds", 0, "most rounds per side while a result is undecided (default 30; equal to -rounds turns adding rounds off)")
	fs.StringVar(&f.build, "build", "", "command run once per side before the rounds")
	fs.StringVar(&f.adapter, "adapter", "", "how to read results: go (Go benchmark output), json (jvq's JSON format) or time (time the whole command)")
	fs.StringVar(&f.output, "output", "", "read results from this file, which the command writes, instead of its standard output")
	fs.StringVar(&f.timeout, "timeout", "", "time limit for each command run, such as 10m")
	fs.StringVar(&f.jsonPath, "json", "", "also write the report as JSON to this file")
	fs.StringVar(&f.mdPath, "markdown", "", "also write the report as Markdown to this file")
	fs.BoolVar(&f.baseMayFail, "base-may-fail", false, "if base fails to build or run, pass with nothing compared instead of failing")
	fs.BoolVar(&f.failOnInconclusive, "fail-on-inconclusive", false, "exit 1 when the result is inconclusive")
	fs.BoolVar(&f.noWarmup, "no-warmup", false, "skip the warm-up run on each side")
	fs.BoolVar(&f.keep, "keep", false, "keep the worktrees afterwards, for debugging")
	fs.BoolVar(&f.quiet, "q", false, "no progress lines")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: jvq run [flags] [-- command]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitPass
		}
		return exitError
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code, err := run(ctx, f, fs.Args(), stdout, stderr)
	if err != nil {
		if ctx.Err() != nil {
			if f.keep {
				fmt.Fprintln(stderr, "jvq: interrupted")
			} else {
				fmt.Fprintln(stderr, "jvq: interrupted; worktrees removed")
			}
			return exitError
		}
		var se *runner.StepError
		if errors.As(err, &se) {
			fmt.Fprintf(stderr, "jvq: %v\n", err)
			if out := strings.TrimSpace(se.Output); out != "" {
				fmt.Fprintf(stderr, "--- output (end) ---\n%s\n---\n", out)
			}
			annotate(stderr, "error", err.Error())
			return exitError
		}
		fmt.Fprintf(stderr, "jvq: %v\n", err)
		annotate(stderr, "error", err.Error())
		return exitError
	}
	return code
}

func run(ctx context.Context, f runFlags, cmdArgs []string, stdout, stderr io.Writer) (int, error) {
	started := time.Now()
	progress := stderr
	if f.quiet {
		progress = nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return exitError, err
	}
	repo, err := gitx.Open(ctx, cwd)
	if err != nil {
		return exitError, err
	}
	sub, err := relativeDir(repo.Root, cwd)
	if err != nil {
		return exitError, err
	}

	cfg, err := loadConfig(f.configPath, cwd, repo.Root)
	if err != nil {
		return exitError, err
	}
	if len(cmdArgs) > 0 {
		cfg.Command = shellJoin(cmdArgs)
	}
	if f.build != "" {
		cfg.Build = f.build
	}
	if f.adapter != "" {
		cfg.Adapter = f.adapter
	}
	if f.output != "" {
		cfg.Output = f.output
	}
	if err := cfg.Check(); err != nil {
		return exitError, err
	}
	if f.rounds != 0 {
		if f.rounds < 3 {
			return exitError, fmt.Errorf("--rounds %d: use at least 3", f.rounds)
		}
		cfg.Rounds = f.rounds
	}
	if f.maxRounds != 0 {
		if f.maxRounds < 3 {
			return exitError, fmt.Errorf("--max-rounds %d: use at least 3", f.maxRounds)
		}
		cfg.MaxRounds = f.maxRounds
	}
	if cfg.MaxRounds < cfg.Rounds {
		cfg.MaxRounds = cfg.Rounds // no extra rounds, and no talk of a lower limit
	}
	if f.timeout != "" {
		d, err := time.ParseDuration(f.timeout)
		if err != nil || d <= 0 {
			return exitError, fmt.Errorf("--timeout %q: want a duration such as 10m", f.timeout)
		}
		cfg.Timeout = d
	}
	if f.noWarmup {
		cfg.Warmup = false
	}
	if f.failOnInconclusive {
		cfg.FailOnInconclusive = true
	}
	if cfg.Command == "" {
		if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err == nil {
			cfg.Command = config.DefaultGoCommand
		} else {
			return exitError, errors.New("no benchmark command: pass one after --, or set command in jvq.toml")
		}
	}

	if n, err := repo.CleanStale(ctx); err == nil && n > 0 && progress != nil {
		fmt.Fprintf(progress, "jvq: removed %d worktree folder(s) left by an earlier run that was killed\n", n)
	}

	headSHA, err := repo.Resolve(ctx, f.head)
	if err != nil {
		return exitError, err
	}
	var pick gitx.Pick
	if f.base != "" {
		sha, err := repo.Resolve(ctx, f.base)
		if err != nil {
			if repo.Shallow(ctx) {
				err = fmt.Errorf("%v; %s", err, gitx.ShallowHint)
			}
			return exitError, err
		}
		pick = gitx.Pick{Ref: f.base, SHA: sha}
	} else {
		pick, err = repo.DefaultBase(ctx, headSHA, os.Getenv)
		if err != nil {
			return exitError, err
		}
	}
	if pick.SHA == headSHA {
		return exitError, fmt.Errorf("base and head are the same commit (%s); nothing to compare", gitx.Short(headSHA))
	}
	if progress != nil {
		fmt.Fprintf(progress, "jvq: base %s (%s), head %s (%s)\n", gitx.Short(pick.SHA), pick.Ref, gitx.Short(headSHA), f.head)
		if pick.Note != "" {
			fmt.Fprintf(progress, "jvq: %s\n", pick.Note)
		}
		if f.head == "HEAD" && repo.Dirty(ctx) {
			fmt.Fprintln(progress, "jvq: note: uncommitted changes aren't measured; jvq compares commits")
		}
		fmt.Fprintf(progress, "jvq: command: %s\n", cfg.Command)
	}

	ws, err := repo.NewWorkspace()
	if err != nil {
		return exitError, err
	}
	if f.keep {
		defer fmt.Fprintf(stderr, "jvq: worktrees kept in %s\n", ws.Dir)
	} else {
		defer ws.Remove()
	}
	baseDir, err := ws.Add(ctx, "base", pick.SHA)
	if err != nil {
		return exitError, err
	}
	headDir, err := ws.Add(ctx, "head", headSHA)
	if err != nil {
		return exitError, err
	}

	res, err := runner.Run(ctx, runner.Options{
		Command: cfg.Command, Build: cfg.Build, Adapter: cfg.Adapter, Output: cfg.Output,
		Rounds: cfg.Rounds, MaxRounds: cfg.MaxRounds, Warmup: cfg.Warmup, Timeout: cfg.Timeout,
		Subdir: sub, Progress: progress,
		More: func(r *runner.Result) bool {
			return verdict.NeedsMore(verdict.Compare(r.Base, r.Head, cfg))
		},
	}, runner.Side{Name: "base", Dir: baseDir}, runner.Side{Name: "head", Dir: headDir})
	if err != nil {
		var se *runner.StepError
		if f.baseMayFail && errors.As(err, &se) && se.Side == "base" && ctx.Err() == nil {
			fmt.Fprintf(stdout, "jvq: base failed (%v), and --base-may-fail is set: nothing compared, passing\n", err)
			annotate(stderr, "warning", "base failed to "+se.Stage+"; nothing compared")
			return exitPass, nil
		}
		return exitError, err
	}

	rows := verdict.Compare(res.Base, res.Head, cfg)
	rep := report.Report{
		JVQ: Version, Mode: report.ModeRun, Verdict: verdict.Overall(rows),
		Base: report.Commit{Ref: pick.Ref, SHA: pick.SHA}, Head: report.Commit{Ref: f.head, SHA: headSHA},
		Note: pick.Note, Command: cfg.Command, Build: cfg.Build, Adapter: cfg.Adapter,
		Rounds: res.Rounds, MaxRounds: cfg.MaxRounds, Warmup: cfg.Warmup, Confidence: cfg.Confidence, Order: res.Order,
		Seconds: time.Since(started).Seconds(), Counts: verdict.Counts(rows), Rows: rows,
	}
	report.Text(stdout, rep)
	if err := writeFiles(f, rep); err != nil {
		// The verdict stands; a report file that couldn't be written
		// shouldn't turn a regression into an error, or a pass into one.
		fmt.Fprintf(stderr, "jvq: couldn't write a report file: %v\n", err)
		annotate(stderr, "warning", "couldn't write a report file: "+err.Error())
	}
	for _, r := range rows {
		if r.Verdict == verdict.Regression {
			annotate(stderr, "error", fmt.Sprintf("%s %s: %s over budget %s", r.Name, r.Metric, report.Pct(r.Change), r.Budget))
		}
	}
	switch rep.Verdict {
	case verdict.Regression:
		return exitRegression, nil
	case verdict.Inconclusive:
		if cfg.FailOnInconclusive {
			return exitRegression, nil
		}
	}
	return exitPass, nil
}

func writeFiles(f runFlags, rep report.Report) error {
	if f.jsonPath != "" {
		if err := writeFile(f.jsonPath, func(w io.Writer) error { return report.JSON(w, rep) }, false); err != nil {
			return err
		}
	}
	if f.mdPath != "" {
		if err := writeFile(f.mdPath, func(w io.Writer) error { report.Markdown(w, rep); return nil }, false); err != nil {
			return err
		}
	}
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		if err := writeFile(p, func(w io.Writer) error { report.Markdown(w, rep); return nil }, true); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(path string, write func(io.Writer) error, appendTo bool) error {
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendTo {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	fh, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	if err := write(fh); err != nil {
		fh.Close()
		return err
	}
	return fh.Close()
}

// annotate prints a GitHub Actions annotation when running there.
func annotate(w io.Writer, level, msg string) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return
	}
	msg = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(msg)
	fmt.Fprintf(w, "::%s title=jvq::%s\n", level, msg)
}

func loadConfig(path, cwd, root string) (config.Config, error) {
	if path == "" {
		path = config.Find(cwd, root)
		if path == "" {
			return config.Default(), nil
		}
	}
	return config.Load(path)
}

// relativeDir is cwd's path inside the repository, so the command runs in
// the same place in each worktree.
func relativeDir(root, cwd string) (string, error) {
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		r = root
	}
	c, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		c = cwd
	}
	rel, err := filepath.Rel(r, c)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("can't place %s inside the repository at %s", cwd, root)
	}
	return rel, nil
}

// shellJoin turns the words after -- back into one shell command. A single
// word is used as it is, so `jvq run -- "go test -bench=. ./..."` works too.
func shellJoin(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = quoteArg(a)
	}
	return strings.Join(quoted, " ")
}

func quoteArg(a string) string {
	if a == "" {
		if runtime.GOOS == "windows" {
			return `""`
		}
		return "''"
	}
	// cmd.exe treats ^ as an escape character, so on Windows an argument
	// with ^ (or %, which expands variables even inside quotes) is quoted;
	// that protects ^ but not %. A single command string after -- is the
	// safest way to pass anything unusual.
	plain := "-_./=:,+@%^"
	if runtime.GOOS == "windows" {
		plain = "-_./=:,+@\\"
	}
	safe := true
	for _, r := range a {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(plain, r)) {
			safe = false
			break
		}
	}
	if safe {
		return a
	}
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

func cmdCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("jvq check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "config file (default: jvq.toml here or in a parent)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitPass
		}
		return exitError
	}
	cwd, _ := os.Getwd()
	root := cwd
	if repo, err := gitx.Open(context.Background(), cwd); err == nil {
		root = repo.Root
	}
	cfg, err := loadConfig(*path, cwd, root)
	if err != nil {
		fmt.Fprintf(stderr, "jvq: %v\n", err)
		return exitError
	}
	src := cfg.Source
	if src == "" {
		src = "no jvq.toml found; these are the defaults"
	}
	command := cfg.Command
	if command == "" {
		command = "(none set; in a Go module: " + config.DefaultGoCommand + ")"
	}
	fmt.Fprintf(stdout, "config:      %s\n", src)
	fmt.Fprintf(stdout, "command:     %s\n", command)
	if cfg.Build != "" {
		fmt.Fprintf(stdout, "build:       %s\n", cfg.Build)
	}
	fmt.Fprintf(stdout, "adapter:     %s\n", cfg.Adapter)
	if cfg.Output != "" {
		fmt.Fprintf(stdout, "output:      %s (read after each run)\n", cfg.Output)
	}
	fmt.Fprintf(stdout, "rounds:      %d, up to %d while undecided (warm-up %v)\n", cfg.Rounds, cfg.MaxRounds, cfg.Warmup)
	fmt.Fprintf(stdout, "confidence:  %g\n", cfg.Confidence)
	fmt.Fprintf(stdout, "timeout:     %s per run\n", cfg.Timeout)
	fmt.Fprintf(stdout, "budgets:     time %s, bytes %s, allocs %s\n", cfg.Budget.Time, cfg.Budget.Bytes, cfg.Budget.Allocs)
	for _, b := range cfg.Bench {
		var parts []string
		if b.Time != nil {
			parts = append(parts, "time "+b.Time.String())
		}
		if b.Bytes != nil {
			parts = append(parts, "bytes "+b.Bytes.String())
		}
		if b.Allocs != nil {
			parts = append(parts, "allocs "+b.Allocs.String())
		}
		fmt.Fprintf(stdout, "  %s: %s\n", b.Pattern, strings.Join(parts, ", "))
	}
	if len(cfg.Ignore) > 0 {
		fmt.Fprintf(stdout, "ignored:     %s\n", strings.Join(cfg.Ignore, ", "))
	}
	fmt.Fprintf(stdout, "inconclusive: %s\n", map[bool]string{true: "fails the run", false: "passes"}[cfg.FailOnInconclusive])
	return exitPass
}
