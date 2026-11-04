// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/just-very-quickly/jvq/internal/flaky"
	"github.com/just-very-quickly/jvq/internal/gitx"
	"github.com/just-very-quickly/jvq/internal/testresults"
)

// flakyReport is jvq flaky's JSON report, documented in docs/reference.md.
type flakyReport struct {
	JVQ         string       `json:"jvq"`
	Mode        string       `json:"mode"` // "flaky"
	Commit      string       `json:"commit,omitempty"`
	History     string       `json:"history,omitempty"`
	HistoryRuns int          `json:"history_runs"` // runs in the history before this one
	Written     bool         `json:"written"`      // this run was added to the history
	Files       []flakyFile  `json:"files,omitempty"`
	Settings    flakyRules   `json:"settings"`
	Notes       []string     `json:"notes,omitempty"`
	Result      flaky.Result `json:"result"`
}

type flakyFile struct {
	Path   string `json:"path"`
	Format string `json:"format"`
	Tests  int    `json:"tests"`
}

type flakyRules struct {
	Window          int `json:"window"`
	QuarantineAfter int `json:"quarantine_after"`
	ReleaseAfter    int `json:"release_after"`
	BrokenAfter     int `json:"broken_after"`
}

// cmdFlaky reads one CI run's test results, judges them against the test
// history, adds the run to the history, and exits 1 only for failures
// that aren't quarantined.
func cmdFlaky(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("jvq flaky", flag.ContinueOnError)
	fs.SetOutput(stderr)
	historyFlag := fs.String("history", "", "the test history file (default: [flaky] history in jvq.toml); - for none")
	commitFlag := fs.String("commit", "", "the commit the results are for (default: git HEAD, else $GITHUB_SHA or $CI_COMMIT_SHA)")
	noWrite := fs.Bool("no-write", false, "judge against the history without adding this run to it")
	configPath := fs.String("config", "", "config file (default: jvq.toml here or in a parent, up to the repository root)")
	jsonPath := fs.String("json", "", "also write the report as JSON to this file")
	mdPath := fs.String("markdown", "", "also write the report as Markdown to this file")
	quiet := fs.Bool("q", false, "print the verdict and notes, not the table")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: jvq flaky [flags] [RESULTS...]")
		fmt.Fprintln(stderr, "\nRESULTS are JUnit XML or go test -json files, or folders of them; together they are one CI run.")
		fmt.Fprintln(stderr, "With none, jvq flaky shows the history's quarantine.")
		fs.PrintDefaults()
	}
	var paths []string
	rest := args
	for {
		if len(rest) > 0 && rest[0] == "--" {
			paths = append(paths, rest[1:]...)
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
		paths = append(paths, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "jvq: %v\n", err)
		annotate(stderr, "error", err.Error())
		return exitError
	}

	cwd, _ := os.Getwd()
	root := cwd
	var repo *gitx.Repo
	if r, err := gitx.Open(context.Background(), cwd); err == nil {
		repo, root = r, r.Root
	}
	cfg, err := loadConfig(*configPath, cwd, root)
	if err != nil {
		return fail(err)
	}
	rules := flaky.Settings{Window: cfg.Flaky.Window, QuarantineAfter: cfg.Flaky.QuarantineAfter, ReleaseAfter: cfg.Flaky.ReleaseAfter, BrokenAfter: cfg.Flaky.BrokenAfter}
	histPath := *historyFlag
	if histPath == "" {
		histPath = cfg.Flaky.History
	}
	if histPath == "-" {
		histPath = ""
	}
	rep := flakyReport{JVQ: Version, Mode: "flaky", History: histPath,
		Settings: flakyRules{rules.Window, rules.QuarantineAfter, rules.ReleaseAfter, rules.BrokenAfter}}

	var entries []flaky.Entry
	if histPath != "" {
		var bad int
		var missing bool
		entries, bad, missing, err = flaky.Load(histPath)
		if err != nil {
			return fail(err)
		}
		if missing && len(paths) > 0 && !*noWrite {
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s doesn't exist yet, so this run starts the history", histPath))
		} else if missing {
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s doesn't exist yet, so nothing is in quarantine", histPath))
		}
		if bad > 0 {
			rep.Notes = append(rep.Notes, fmt.Sprintf("skipped %d damaged line(s) of %s; the other runs still count", bad, histPath))
			annotate(stderr, "warning", fmt.Sprintf("skipped %d damaged line(s) of the test history %s", bad, histPath))
		}
	}
	rep.HistoryRuns = len(entries)

	if len(paths) == 0 {
		if histPath == "" {
			fmt.Fprintln(stderr, "jvq flaky: give results files, or a history (-history or [flaky] history in jvq.toml) to show")
			fs.Usage()
			return exitError
		}
		rep.Result = flaky.Judge(entries, nil, rules)
		writeFlaky(stdout, rep, false, *quiet)
		if err := writeFlakyFiles(rep, *jsonPath, *mdPath); err != nil {
			fmt.Fprintf(stderr, "jvq: couldn't write a report file: %v\n", err)
		}
		return exitPass
	}

	run, err := testresults.Collect(paths)
	if err != nil {
		return fail(err)
	}
	for _, f := range run.Files {
		rep.Files = append(rep.Files, flakyFile{f.Path, f.Format, f.Tests})
	}
	for _, sk := range run.Skipped {
		rep.Notes = append(rep.Notes, fmt.Sprintf("skipped %s in a results folder: %s", sk.Path, sk.Reason))
	}
	if len(run.Attempts) == 0 {
		return fail(fmt.Errorf("no test results in %s; did the tests run?", strings.Join(paths, ", ")))
	}
	tests, failed := 0, false
	for id, a := range run.Attempts {
		if testresults.IsTest(id) {
			tests++
		}
		failed = failed || strings.IndexByte(a, testresults.Fail) >= 0
	}
	if tests == 0 && !failed {
		// Only Go packages that passed with no tests in them: a -run
		// filter that matched nothing, say.
		return fail(fmt.Errorf("no test ran in %s, only packages with no tests; did the tests run?", strings.Join(paths, ", ")))
	}

	// The checkout's HEAD comes before the CI variables: it's the code the
	// tests ran on when they ran here, while GITHUB_SHA can be another
	// commit (on pull_request_target and workflow_run it's the base
	// branch's latest, the same for every pull request).
	commit := *commitFlag
	if commit == "" && repo != nil {
		if sha, err := repo.Resolve(context.Background(), "HEAD"); err == nil {
			commit = sha
		}
	}
	if commit == "" {
		commit = os.Getenv("GITHUB_SHA")
	}
	if commit == "" {
		commit = os.Getenv("CI_COMMIT_SHA")
	}
	if commit == "" {
		rep.Notes = append(rep.Notes, "no commit known (-commit, a git checkout, GITHUB_SHA or CI_COMMIT_SHA), so only this run's own retries count as the same code")
	}
	rep.Commit = commit
	if histPath == "" {
		rep.Notes = append(rep.Notes, "no test history (-history or [flaky] history in jvq.toml), so nothing is quarantined; only this run's retries are judged")
	}

	entry := flaky.NewEntry(Version, time.Now(), commit, run.Attempts)
	rep.Result = flaky.Judge(entries, &entry, rules)
	if histPath != "" && !*noWrite {
		if err := flaky.Append(histPath, entry, cfg.Flaky.HistoryKeep); err != nil {
			// The verdict stands; a history that couldn't be written
			// shouldn't turn a pass into an error, or a failure into one.
			fmt.Fprintf(stderr, "jvq: couldn't add to the test history: %v\n", err)
			annotate(stderr, "warning", "couldn't add to the test history: "+err.Error())
		} else {
			rep.Written = true
		}
	}

	writeFlaky(stdout, rep, false, *quiet)
	if err := writeFlakyFiles(rep, *jsonPath, *mdPath); err != nil {
		fmt.Fprintf(stderr, "jvq: couldn't write a report file: %v\n", err)
		annotate(stderr, "warning", "couldn't write a report file: "+err.Error())
	}
	for _, r := range rep.Result.Rows {
		switch r.Status {
		case flaky.StatusFailed:
			annotate(stderr, "error", showID(r.ID)+" failed")
		case flaky.StatusBroken:
			annotate(stderr, "error", fmt.Sprintf("%s failed %d attempts in a row: broken, not flaky, so it left quarantine and blocks", showID(r.ID), r.FailStreak))
		case flaky.StatusQuarantined:
			annotate(stderr, "warning", showID(r.ID)+" failed; it's in quarantine as flaky, so it didn't block")
		}
	}
	if n := rep.Result.NotRun; n > 0 {
		var names []string
		for _, r := range rep.Result.Rows {
			if r.Status == flaky.StatusNotRun {
				names = append(names, showID(r.ID))
			}
		}
		if len(names) > 5 {
			names = append(names[:5], "...")
		}
		annotate(stderr, "warning", fmt.Sprintf("%d test(s) didn't run because their package stopped (a crash or a timeout), so they weren't checked: %s", n, strings.Join(names, ", ")))
	}
	if rep.Result.Verdict == flaky.VerdictFail {
		return exitRegression
	}
	return exitPass
}

func writeFlakyFiles(rep flakyReport, jsonPath, mdPath string) error {
	if jsonPath != "" {
		if err := writeFile(jsonPath, func(w io.Writer) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(rep)
		}, false); err != nil {
			return err
		}
	}
	if mdPath != "" {
		if err := writeFile(mdPath, func(w io.Writer) error { writeFlaky(w, rep, true, false); return nil }, false); err != nil {
			return err
		}
	}
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		if err := writeFile(p, func(w io.Writer) error { writeFlaky(w, rep, true, false); return nil }, true); err != nil {
			return err
		}
	}
	return nil
}

// statusText says what a row's status means for the gate.
func statusText(r flaky.Row, s flakyRules, inRun bool) string {
	var t string
	switch r.Status {
	case flaky.StatusFailed:
		t = "failed, blocks"
	case flaky.StatusBroken:
		t = fmt.Sprintf("broken (%d failed attempts in a row), left quarantine, blocks", r.FailStreak)
	case flaky.StatusQuarantined:
		t = "failed in quarantine, doesn't block"
	case flaky.StatusFlaky:
		t = "failed, passed on retry"
	case flaky.StatusSkipped:
		t = "skipped"
	case flaky.StatusNotRun:
		t = "not run: its package stopped before it (a crash or a timeout)"
	case flaky.StatusAbsent:
		if !inRun {
			t = "quarantined"
			break
		}
		t = "not in this run"
	default:
		t = "passed"
	}
	switch r.Event {
	case flaky.EventEntered:
		t += "; into quarantine"
	case flaky.EventReleased:
		t += fmt.Sprintf("; left quarantine (%d passing runs in a row)", s.ReleaseAfter)
	case flaky.EventBroken:
		if r.Status != flaky.StatusBroken {
			t += fmt.Sprintf("; left quarantine as broken (%d failed attempts in a row)", r.FailStreak)
		}
	}
	if r.Quarantined && r.Event != flaky.EventEntered {
		switch r.Status {
		case flaky.StatusQuarantined:
			// says so already, and its pass streak is 0
		case flaky.StatusFlaky:
			t += "; in quarantine"
		default:
			if t != "quarantined" {
				t += "; in quarantine"
			}
			t += fmt.Sprintf(", %d of %d passes to leave", r.PassStreak, s.ReleaseAfter)
		}
	}
	return t
}

// showID is a test's name as printed. Test names come from the results
// and the history, which a pull request can write, so a name that has a
// character that isn't printable (a line break, a tab) or starts with ":"
// is printed quoted: it can't break a line or begin one with "::", which
// GitHub Actions would take for a workflow command.
func showID(id string) string {
	if id == "" || strings.HasPrefix(id, ":") || strings.IndexFunc(id, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return strconv.Quote(id)
	}
	return id
}

// flakyMD escapes text for the Markdown report, HTML too, since test names
// can come from a pull request.
func flakyMD(s string) string {
	return strings.ReplaceAll(mdCell(s), "<", "&lt;")
}

// verdictLine sums up the run in one sentence.
func verdictLine(rep flakyReport) string {
	res := rep.Result
	if len(rep.Files) == 0 {
		return fmt.Sprintf("%d test(s) in quarantine.", res.InQuarantine)
	}
	var parts []string
	blocking := res.Failed + res.Broken
	switch {
	case blocking > 0:
		var names []string
		for _, r := range res.Rows {
			if r.Blocks {
				names = append(names, showID(r.ID))
			}
		}
		if len(names) > 3 {
			names = append(names[:3], "...")
		}
		parts = append(parts, fmt.Sprintf("FAIL: %d test(s) failed (%s).", blocking, strings.Join(names, ", ")))
	default:
		parts = append(parts, "PASS: no test failed outside quarantine.")
	}
	if res.Quarantined > 0 {
		parts = append(parts, fmt.Sprintf("%d quarantined test(s) failed and didn't block.", res.Quarantined))
	}
	if res.Flaky > 0 {
		parts = append(parts, fmt.Sprintf("%d test(s) failed and passed on retry.", res.Flaky))
	}
	if res.NotRun > 0 {
		parts = append(parts, fmt.Sprintf("%d test(s) didn't run because their package stopped.", res.NotRun))
	}
	parts = append(parts, fmt.Sprintf("%d test(s) in quarantine.", res.InQuarantine))
	return strings.Join(parts, " ")
}

func writeFlaky(w io.Writer, rep flakyReport, md, quiet bool) {
	res := rep.Result
	var head string
	if len(rep.Files) > 0 {
		formats := map[string]int{}
		for _, f := range rep.Files {
			formats[f.Format]++
		}
		var fl []string
		for _, k := range []string{testresults.JUnit, testresults.GoJSON} {
			if n := formats[k]; n > 0 {
				name := map[string]string{testresults.JUnit: "JUnit XML", testresults.GoJSON: "go test -json"}[k]
				fl = append(fl, fmt.Sprintf("%d %s", n, name))
			}
		}
		head = fmt.Sprintf("%d tests from %s", res.Tests, strings.Join(fl, " and "))
		if rep.Commit != "" {
			head += ", commit " + gitx.Short(rep.Commit)
		}
	} else {
		head = "quarantine"
	}
	if rep.History != "" {
		head += fmt.Sprintf("; history %s, %d run(s) before this one", rep.History, rep.HistoryRuns)
		if len(rep.Files) == 0 {
			head = fmt.Sprintf("quarantine in %s, %d run(s)", rep.History, rep.HistoryRuns)
		}
	}
	legend := "LAST RUNS: p pass, f fail, x failed then passed on retry, s skipped; latest on the right."
	if md {
		fmt.Fprintf(w, "### jvq flaky\n\n**%s**\n\n%s.\n\n", flakyMD(verdictLine(rep)), head)
		if len(res.Rows) > 0 {
			fmt.Fprintln(w, "| Test | This run | Status | Flaky commits | Last runs |")
			fmt.Fprintln(w, "|---|---|---|---:|---|")
			for _, r := range res.Rows {
				fmt.Fprintf(w, "| %s | %s | %s | %d | `%s` |\n", flakyMD(showID(r.ID)), dash(r.Attempts), statusText(r, rep.Settings, len(rep.Files) > 0), r.FlakyCommits, r.Recent)
			}
			fmt.Fprintf(w, "\n_%s A test goes into quarantine after %d commits on which it both passed and failed, in its last %d runs; it leaves after %d passing runs in a row, or as broken after %d failed attempts in a row._\n",
				strings.Replace(legend, "LAST RUNS", "Last runs", 1), rep.Settings.QuarantineAfter, rep.Settings.Window, rep.Settings.ReleaseAfter, rep.Settings.BrokenAfter)
		}
		for _, n := range rep.Notes {
			fmt.Fprintf(w, "\n_Note: %s._\n", n)
		}
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintf(w, "\njvq flaky: %s\n", head)
	if len(res.Rows) > 0 && !quiet {
		fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 2, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "TEST\tTHIS RUN\tSTATUS\tFLAKY COMMITS\tLAST RUNS")
		for _, r := range res.Rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", showID(r.ID), dash(r.Attempts), statusText(r, rep.Settings, len(rep.Files) > 0), r.FlakyCommits, r.Recent)
		}
		tw.Flush()
		fmt.Fprintf(w, "\n%s\n", legend)
		fmt.Fprintf(w, "Quarantine: %d flaky commits (passed and failed on one commit) in a test's last %d runs; out after %d passing runs in a row, or as broken after %d failed attempts in a row.\n",
			rep.Settings.QuarantineAfter, rep.Settings.Window, rep.Settings.ReleaseAfter, rep.Settings.BrokenAfter)
	}
	fmt.Fprintf(w, "\n%s\n", verdictLine(rep))
	for _, n := range rep.Notes {
		fmt.Fprintf(w, "Note: %s.\n", n)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
