// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/just-very-quickly/jvq/internal/config"
	"github.com/just-very-quickly/jvq/internal/gitx"
	"github.com/just-very-quickly/jvq/internal/history"
)

// cmdHistory reads a history file and shows each benchmark's trend: how
// many runs measured it, its change run by run, and those changes chained
// together. It can fail when the chained change of any gated metric is
// over a limit, which catches a slowdown that crept in by small steps,
// each one within its budget.
func cmdHistory(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("jvq history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	last := fs.Int("n", 0, "use only the newest N runs (default: all)")
	bench := fs.String("bench", "", "only benchmarks matching this pattern (* matches anything)")
	metric := fs.String("metric", "", "only this metric, such as time")
	failOver := fs.String("fail-over", "", "exit 1 if any gated result has moved more than this in all, beyond its noise, such as 10%")
	mdPath := fs.String("markdown", "", "also write the table as Markdown to this file")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: jvq history [flags] FILE")
		fmt.Fprintln(stderr, "\nFILE is a history file written by jvq run -history.")
		fs.PrintDefaults()
	}
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
		files = append(files, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if *last < 0 {
		fmt.Fprintf(stderr, "jvq history: -n %d: use 1 or more, or leave it out for all runs\n", *last)
		return exitError
	}
	if len(files) != 1 {
		fmt.Fprintf(stderr, "jvq history: want one history file; got %d\n", len(files))
		fs.Usage()
		return exitError
	}
	var limit *config.Budget
	if *failOver != "" {
		b, err := config.ParseBudget("time", *failOver)
		if err != nil || b.Off {
			fmt.Fprintf(stderr, "jvq history: -fail-over %q: want a percentage such as 10%%\n", *failOver)
			return exitError
		}
		limit = &b
	}
	fh, err := os.Open(files[0])
	if err != nil {
		fmt.Fprintf(stderr, "jvq: %v\n", err)
		return exitError
	}
	entries, bad, err := history.Read(fh)
	fh.Close()
	if err != nil {
		fmt.Fprintf(stderr, "jvq: %s: %v\n", files[0], err)
		return exitError
	}
	if bad > 0 {
		fmt.Fprintf(stderr, "jvq: note: skipped %d line(s) of %s that aren't jvq history entries\n", bad, files[0])
	}
	if *last > 0 && len(entries) > *last {
		entries = entries[len(entries)-*last:]
	}
	if len(entries) == 0 {
		fmt.Fprintf(stdout, "jvq history: no runs in %s yet\n", files[0])
		return exitPass
	}
	var trends []history.Trend
	for _, t := range history.Trends(entries) {
		if *metric != "" && t.Metric != *metric {
			continue
		}
		key := t.Name
		if t.Package != "" {
			key = t.Package + "." + t.Name
		}
		if *bench != "" && !config.Match(*bench, t.Name) && !config.Match(*bench, key) {
			continue
		}
		trends = append(trends, t)
	}
	over := 0
	if limit != nil {
		for _, t := range trends {
			if t.Over(limit.Val) {
				over++
			}
		}
	}
	gaps := history.Gaps(entries)
	writeHistory(stdout, files[0], entries, trends, limit, over, gaps, false)
	if *mdPath != "" {
		if err := writeFile(*mdPath, func(w io.Writer) error {
			writeHistory(w, files[0], entries, trends, limit, over, gaps, true)
			return nil
		}, false); err != nil {
			fmt.Fprintf(stderr, "jvq: couldn't write %s: %v\n", *mdPath, err)
			return exitError
		}
	}
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		writeFile(p, func(w io.Writer) error {
			writeHistory(w, files[0], entries, trends, limit, over, gaps, true)
			return nil
		}, true)
	}
	if over > 0 {
		annotate(stderr, "error", fmt.Sprintf("%d benchmark(s) moved more than %s across %d runs", over, limit.Raw, len(entries)))
		return exitRegression
	}
	return exitPass
}

// sparkRuns is how many of the latest runs the run-by-run column shows.
const sparkRuns = 30

func writeHistory(w io.Writer, file string, entries []history.Entry, trends []history.Trend, limit *config.Budget, over, gaps int, md bool) {
	first, last := entries[0], entries[len(entries)-1]
	span := fmt.Sprintf("%d runs, %s (%s) to %s (%s)", len(entries), day(first.Date), gitx.Short(first.Head.SHA), day(last.Date), gitx.Short(last.Head.SHA))
	if len(entries) == 1 {
		span = fmt.Sprintf("1 run, %s (%s)", day(first.Date), gitx.Short(first.Head.SHA))
	}
	inAll := func(t history.Trend) string {
		v := history.Pct(t.Compounded)
		if t.HasInterval && len(t.Changes) > 0 && t.Lo != t.Hi {
			v += " (" + history.Pct(t.Lo) + " … " + history.Pct(t.Hi) + ")"
		}
		if limit != nil && t.Over(limit.Val) {
			v += " OVER " + limit.Raw
		}
		return v
	}
	if md {
		fmt.Fprintf(w, "### jvq history\n\n%s in `%s`. *In all* chains each run's change, with a 95%% interval that widens as the runs' noise adds up.\n\n", span, file)
		fmt.Fprintln(w, "| Benchmark | Metric | Runs | In all | Regressions | Run by run |")
		fmt.Fprintln(w, "|---|---|---:|---|---:|---|")
	} else {
		fmt.Fprintf(w, "\njvq history: %s in %s\n\n", span, file)
	}
	tw := tabwriter.NewWriter(w, 2, 0, 2, ' ', 0)
	if !md {
		fmt.Fprintln(tw, "BENCHMARK\tMETRIC\tRUNS\tIN ALL\tREGRESSIONS\tRUN BY RUN")
	}
	for _, t := range trends {
		name := t.Name
		if t.Package != "" && collides(trends, t) {
			name = t.Package + "." + t.Name
		}
		changes := t.Changes
		if len(changes) > sparkRuns {
			changes = changes[len(changes)-sparkRuns:]
		}
		if md {
			fmt.Fprintf(w, "| %s | %s | %d | %s | %d | %s |\n", mdCell(name), t.Metric, len(t.Changes), inAll(t), t.Regressions, history.Spark(changes))
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%d\t%s\n", name, t.Metric, len(t.Changes), inAll(t), t.Regressions, history.Spark(changes))
		}
	}
	tw.Flush()
	var notes []string
	if gaps > 0 {
		notes = append(notes, fmt.Sprintf("%d run(s) didn't start where the one before ended, so some commits in between weren't measured", gaps))
	}
	if limit != nil {
		if over > 0 {
			notes = append(notes, fmt.Sprintf("%d result(s) moved more than %s in all, the whole interval over it", over, limit.Raw))
		} else {
			notes = append(notes, fmt.Sprintf("nothing moved more than %s in all beyond its noise", limit.Raw))
		}
	}
	if md {
		for _, n := range notes {
			fmt.Fprintf(w, "\n_%s._\n", capitalise(n))
		}
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintln(w, "\nIN ALL chains each run's change, base to head, with a 95% interval that widens as the runs' noise adds up.")
	fmt.Fprintf(w, "RUN BY RUN shows the latest %d: · no change, ▁ faster or smaller, taller blocks slower or bigger.\n", sparkRuns)
	for _, n := range notes {
		fmt.Fprintf(w, "\n%s.\n", capitalise(n))
	}
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func collides(trends []history.Trend, t history.Trend) bool {
	for _, o := range trends {
		if o.Name == t.Name && o.Package != t.Package {
			return true
		}
	}
	return false
}

func day(rfc string) string {
	if len(rfc) >= 10 {
		return rfc[:10]
	}
	return rfc
}

func mdCell(s string) string {
	return strings.NewReplacer("|", `\|`, "_", `\_`, "*", `\*`).Replace(s)
}
