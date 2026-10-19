// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/just-very-quickly/jvq/internal/adapter"
	"github.com/just-very-quickly/jvq/internal/gitx"
	"github.com/just-very-quickly/jvq/internal/report"
	"github.com/just-very-quickly/jvq/internal/verdict"
)

// cmdCompare judges two saved benchmark outputs, such as
// `go test -bench=. -count=10 > old.txt` before a change and new.txt after
// it, with the same statistics and budgets as jvq run. It can't control for
// a machine that changed between the two, which is what run's interleaved
// rounds are for, so it suits outputs taken back to back on one machine.
func cmdCompare(args []string, stdout, stderr io.Writer) int {
	var f runFlags
	fs := flag.NewFlagSet("jvq compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.configPath, "config", "", "config file for budgets and ignores (default: jvq.toml here or in a parent)")
	fs.StringVar(&f.adapter, "adapter", "", "format of the files: go or json (default: json for .json and .jsonl files, else the config's adapter, else go)")
	fs.StringVar(&f.jsonPath, "json", "", "also write the report as JSON to this file")
	fs.StringVar(&f.mdPath, "markdown", "", "also write the report as Markdown to this file")
	fs.BoolVar(&f.failOnInconclusive, "fail-on-inconclusive", false, "exit 1 when the result is inconclusive")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: jvq compare [flags] OLD NEW")
		fmt.Fprintln(stderr, "\nOLD and NEW are saved outputs of the same benchmarks, each with several")
		fmt.Fprintln(stderr, "runs (go test -count=10, or several json values per result).")
		fs.PrintDefaults()
	}
	// Flags may come after the file names too: jvq compare old new -json r.json.
	var files []string
	rest := args
	for {
		if len(rest) > 0 && rest[0] == "--" {
			files = append(files, rest[1:]...)
			break
		}
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return exitPass
			}
			return exitError
		}
		if fs.NArg() == 0 {
			break
		}
		// Parse stops at the first file name, or after a "--" it removes:
		// then everything left is file names.
		if n := len(rest) - fs.NArg(); n > 0 && rest[n-1] == "--" {
			files = append(files, fs.Args()...)
			break
		}
		files = append(files, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(files) != 2 {
		fmt.Fprintf(stderr, "jvq compare: want two files, OLD and NEW; got %d\n", len(files))
		fs.Usage()
		return exitError
	}
	code, err := compare(f, files[0], files[1], stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "jvq: %v\n", err)
		annotate(stderr, "error", err.Error())
		return exitError
	}
	return code
}

func compare(f runFlags, oldPath, newPath string, stdout, stderr io.Writer) (int, error) {
	started := time.Now()
	cwd, err := os.Getwd()
	if err != nil {
		return exitError, err
	}
	root := cwd
	if repo, err := gitx.Open(context.Background(), cwd); err == nil {
		root = repo.Root
	}
	cfg, err := loadConfig(f.configPath, cwd, root)
	if err != nil {
		return exitError, err
	}
	if f.failOnInconclusive {
		cfg.FailOnInconclusive = true
	}
	name := compareAdapter(f.adapter, cfg.Adapter, cfg.Source != "", oldPath, newPath)
	if !adapter.Known(name) {
		return exitError, fmt.Errorf("adapter %q: jvq has %s", name, strings.Join(adapter.Names, ", "))
	}
	if adapter.Timed(name) {
		return exitError, errors.New("the time adapter times a command as it runs, so it can't read saved files; use jvq run, or save go or json output")
	}
	cfg.Adapter = name
	base, err := readSamples(name, oldPath)
	if err != nil {
		return exitError, err
	}
	head, err := readSamples(name, newPath)
	if err != nil {
		return exitError, err
	}
	rows := verdict.Compare(base, head, cfg)
	note := "two saved outputs, not interleaved: a machine that changed between them shows up as a change"
	if n := verdict.UngatedNote(rows); n != "" {
		note += "; " + n
	}
	rep := report.Report{
		JVQ: Version, Mode: report.ModeCompare, Verdict: verdict.Overall(rows),
		Base: report.Commit{Ref: oldPath}, Head: report.Commit{Ref: newPath},
		Note:    note,
		Adapter: name, Confidence: cfg.Confidence, Order: []string{},
		Seconds: time.Since(started).Seconds(), Counts: verdict.Counts(rows), Rows: rows,
	}
	report.Text(stdout, rep)
	if err := writeFiles(f, rep); err != nil {
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

// compareAdapter picks the files' format: the flag, then the extension,
// then an adapter set in jvq.toml, then go.
func compareAdapter(flagValue, configured string, fromFile bool, paths ...string) string {
	if flagValue != "" {
		return flagValue
	}
	allJSON := true
	for _, p := range paths {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".json", ".jsonl":
		default:
			allJSON = false
		}
	}
	if allJSON {
		return "json"
	}
	if fromFile && configured != "" && !adapter.Timed(configured) {
		return configured
	}
	return "go"
}

func readSamples(name, path string) ([]adapter.Sample, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	samples, err := adapter.Parse(name, fh)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("%s: no results in the %s format; name the format with -adapter if it's another", path, name)
	}
	return samples, nil
}
