// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package report writes a run's outcome as a terminal table, as Markdown
// (for a CI job summary or a pull request comment) and as JSON.
package report

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/just-very-quickly/jvq/internal/adapter"
	"github.com/just-very-quickly/jvq/internal/gitx"
	"github.com/just-very-quickly/jvq/internal/verdict"
)

// Commit is one side of the comparison.
type Commit struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// Report is everything about a finished run. Its JSON form is documented in
// docs/reference.md.
type Report struct {
	JVQ        string         `json:"jvq"`
	Mode       string         `json:"mode"` // "run", or "compare" for two saved outputs
	Verdict    string         `json:"verdict"`
	Base       Commit         `json:"base"`
	Head       Commit         `json:"head"`
	Note       string         `json:"note,omitempty"`
	Command    string         `json:"command"`
	Build      string         `json:"build,omitempty"`
	Adapter    string         `json:"adapter"`
	Rounds     int            `json:"rounds"`     // rounds per side that ran
	MaxRounds  int            `json:"max_rounds"` // the cap on adaptive rounds
	Warmup     bool           `json:"warmup"`
	Confidence float64        `json:"confidence"`
	Order      []string       `json:"order"`
	Seconds    float64        `json:"seconds"`
	Counts     map[string]int `json:"counts"`
	Rows       []verdict.Row  `json:"results"`
}

// Report modes.
const (
	ModeRun     = "run"
	ModeCompare = "compare"
)

// Value formats a metric value in its unit, scaling times to ns, µs, ms or s.
func Value(metric, unit string, v float64) string {
	if metric == adapter.Time {
		return Duration(v)
	}
	switch metric {
	case adapter.Bytes:
		return trim(v) + " B"
	case adapter.Allocs:
		return trim(v)
	case adapter.Size:
		return Size(v)
	}
	return trim(v) + " " + unit
}

// Size formats a file size in bytes, in decimal units as file managers
// show them.
func Size(b float64) string {
	switch a := math.Abs(b); {
	case a >= 1e9:
		return fmt.Sprintf("%.4g GB", b/1e9)
	case a >= 1e6:
		return fmt.Sprintf("%.4g MB", b/1e6)
	case a >= 1e4:
		return fmt.Sprintf("%.4g kB", b/1e3)
	}
	return trim(b) + " B"
}

// Duration formats nanoseconds.
func Duration(ns float64) string {
	switch a := math.Abs(ns); {
	case a >= 1e9:
		return trim3(ns/1e9) + " s"
	case a >= 1e6:
		return trim3(ns/1e6) + " ms"
	case a >= 1e3:
		return trim3(ns/1e3) + " µs"
	}
	return trim3(ns) + " ns"
}

func trim3(v float64) string { return fmt.Sprintf("%.3g", v) }

func trim(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.4g", v)
}

// Pct formats a relative change such as 0.072 as "+7.2%".
func Pct(f *float64) string {
	if f == nil {
		return ""
	}
	v := *f
	switch {
	case math.IsInf(v, 1):
		return "new"
	case math.Abs(v) < 0.0005:
		return "0.0%"
	}
	return fmt.Sprintf("%+.1f%%", v*100)
}

// Interval formats a confidence interval such as "+5.1% … +9.0%".
func Interval(lo, hi *float64) string {
	if lo == nil || hi == nil {
		return ""
	}
	return Pct(lo) + " … " + Pct(hi)
}

func side(r verdict.Row, s *verdict.Side) string {
	if s == nil {
		return "-"
	}
	return Value(r.Metric, r.Unit, s.Median)
}

func label(v string) string {
	if v == verdict.Regression {
		return "REGRESSION"
	}
	return v
}

// Text writes the terminal table.
func Text(w io.Writer, rep Report) {
	if rep.Mode == ModeCompare {
		fmt.Fprintf(w, "\njvq %s: old %s vs new %s, %.0f%% intervals\n", rep.JVQ, rep.Base.Ref, rep.Head.Ref, rep.Confidence*100)
	} else {
		fmt.Fprintf(w, "\njvq %s: base %s (%s) vs head %s (%s), %d rounds, %.0f%% intervals\n",
			rep.JVQ, gitx.Short(rep.Base.SHA), rep.Base.Ref, gitx.Short(rep.Head.SHA), rep.Head.Ref, rep.Rounds, rep.Confidence*100)
	}
	if rep.MaxRounds > 0 && rep.Rounds >= rep.MaxRounds && rep.Verdict == verdict.Inconclusive {
		fmt.Fprintf(w, "note: still undecided at the %d-round limit; raise max_rounds or loosen the budget to settle it\n", rep.MaxRounds)
	}
	if rep.Note != "" {
		fmt.Fprintf(w, "note: %s\n", rep.Note)
	}
	fmt.Fprintln(w)
	shown := names(rep.Rows)
	tw := tabwriter.NewWriter(w, 2, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BENCHMARK\tMETRIC\tBASE\tHEAD\tCHANGE\tINTERVAL\tBUDGET\tVERDICT")
	for _, r := range rep.Rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", shown[r.Key()], r.Metric, side(r, r.Base), side(r, r.Head),
			Pct(r.Change), Interval(r.Lo, r.Hi), r.Budget, label(r.Verdict))
	}
	tw.Flush()
	fmt.Fprintf(w, "\nverdict: %s (%s) in %s\n", label(rep.Verdict), summary(rep.Counts),
		(time.Duration(rep.Seconds * float64(time.Second))).Round(100*time.Millisecond))
	for _, r := range rep.Rows {
		if r.Note != "" && (r.Verdict == verdict.Regression || r.Verdict == verdict.Inconclusive) {
			fmt.Fprintf(w, "  %s %s: %s\n", shown[r.Key()], r.Metric, r.Note)
		}
	}
}

// names maps each row's key to the name shown: the benchmark name alone, or
// with its package when two packages have a benchmark of that name. The
// JSON always has both.
func names(rows []verdict.Row) map[string]string {
	pkgs := map[string]map[string]bool{}
	for _, r := range rows {
		if pkgs[r.Name] == nil {
			pkgs[r.Name] = map[string]bool{}
		}
		pkgs[r.Name][r.Package] = true
	}
	out := map[string]string{}
	for _, r := range rows {
		if len(pkgs[r.Name]) > 1 {
			out[r.Key()] = r.Key()
		} else {
			out[r.Key()] = r.Name
		}
	}
	return out
}

var order = []string{verdict.Regression, verdict.Inconclusive, verdict.Pass, verdict.Improved,
	verdict.New, verdict.Removed, verdict.Info}

func summary(c map[string]int) string {
	var parts []string
	for _, k := range order {
		if n := c[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
		}
	}
	if len(parts) == 0 {
		return "no results"
	}
	return strings.Join(parts, ", ")
}

// Markdown writes a summary for a CI job summary page or a pull request.
func Markdown(w io.Writer, rep Report) {
	head := map[string]string{
		verdict.Pass:         "jvq: pass",
		verdict.Regression:   "jvq: regression",
		verdict.Inconclusive: "jvq: inconclusive",
	}[rep.Verdict]
	fmt.Fprintf(w, "### %s\n\n", head)
	if rep.Mode == ModeCompare {
		fmt.Fprintf(w, "Old `%s` against new `%s`, %.0f%% intervals. %s.\n\n",
			mdCode(rep.Base.Ref), mdCode(rep.Head.Ref), rep.Confidence*100, capitalise(summary(rep.Counts)))
	} else {
		fmt.Fprintf(w, "Base `%s` (%s) against head `%s` (%s), %d interleaved rounds, %.0f%% intervals. %s.\n\n",
			gitx.Short(rep.Base.SHA), md(rep.Base.Ref), gitx.Short(rep.Head.SHA), md(rep.Head.Ref), rep.Rounds,
			rep.Confidence*100, capitalise(summary(rep.Counts)))
	}
	if rep.Note != "" {
		fmt.Fprintf(w, "_%s._\n\n", md(rep.Note))
	}
	shown := names(rep.Rows)
	fmt.Fprintln(w, "| Benchmark | Metric | Base | Head | Change | Interval | Budget | Verdict |")
	fmt.Fprintln(w, "|---|---|---:|---:|---:|---|---|---|")
	for _, r := range rep.Rows {
		v := r.Verdict
		if v == verdict.Regression {
			v = "**regression**"
		}
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s | %s |\n", md(shown[r.Key()]), md(r.Metric),
			md(side(r, r.Base)), md(side(r, r.Head)), md(Pct(r.Change)), md(Interval(r.Lo, r.Hi)), md(r.Budget), v)
	}
	fmt.Fprintln(w)
}

// mdCode makes s safe inside a Markdown code span.
func mdCode(s string) string {
	return strings.NewReplacer("`", "'", "\n", " ").Replace(s)
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// md escapes characters that break a Markdown table cell.
func md(s string) string {
	r := strings.NewReplacer("|", `\|`, "\n", " ", "`", "'", "*", `\*`, "_", `\_`)
	return r.Replace(s)
}

// JSON writes the report as indented JSON. Infinite changes (from zero to
// something) are written as null in the change field.
func JSON(w io.Writer, rep Report) error {
	rows := make([]verdict.Row, len(rep.Rows))
	for i, r := range rep.Rows {
		if r.Change != nil && (math.IsInf(*r.Change, 0) || math.IsNaN(*r.Change)) {
			r.Change = nil
			if r.Note == "" {
				r.Note = "base was zero"
			}
		}
		rows[i] = r
	}
	rep.Rows = rows
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// JUnit writes the report as a JUnit XML test report, which GitLab shows in
// the merge request and Jenkins and other CI systems read too. Each gated
// result is a test case: a regression fails it, inconclusive skips it with
// the reason, and pass or improved passes. Results that aren't gated (new,
// removed, info) aren't listed.
func JUnit(w io.Writer, rep Report) error {
	type failure struct {
		Message string `xml:"message,attr"`
		Type    string `xml:"type,attr"`
		Text    string `xml:",chardata"`
	}
	type skipped struct {
		Message string `xml:"message,attr"`
	}
	type testcase struct {
		Classname string   `xml:"classname,attr"`
		Name      string   `xml:"name,attr"`
		Time      string   `xml:"time,attr"`
		Failure   *failure `xml:"failure,omitempty"`
		Skipped   *skipped `xml:"skipped,omitempty"`
		SystemOut string   `xml:"system-out,omitempty"`
	}
	type testsuite struct {
		XMLName  xml.Name   `xml:"testsuite"`
		Name     string     `xml:"name,attr"`
		Tests    int        `xml:"tests,attr"`
		Failures int        `xml:"failures,attr"`
		Errors   int        `xml:"errors,attr"`
		Skipped  int        `xml:"skipped,attr"`
		Time     string     `xml:"time,attr"`
		Cases    []testcase `xml:"testcase"`
	}
	type testsuites struct {
		XMLName xml.Name    `xml:"testsuites"`
		Name    string      `xml:"name,attr"`
		Suites  []testsuite `xml:"testsuite"`
	}
	suite := testsuite{Name: "jvq", Time: fmt.Sprintf("%.3f", rep.Seconds)}
	shown := names(rep.Rows)
	for _, r := range rep.Rows {
		if !r.Gated() {
			continue
		}
		class := "jvq"
		if r.Package != "" {
			class = "jvq." + r.Package
		}
		detail := fmt.Sprintf("base %s, head %s, change %s", side(r, r.Base), side(r, r.Head), Pct(r.Change))
		if iv := Interval(r.Lo, r.Hi); iv != "" {
			detail += ", interval " + iv
		}
		detail += ", budget " + r.Budget
		if r.Note != "" {
			detail += "; " + r.Note
		}
		tc := testcase{Classname: class, Name: shown[r.Key()] + " " + r.Metric, Time: "0", SystemOut: detail}
		switch r.Verdict {
		case verdict.Regression:
			suite.Failures++
			tc.Failure = &failure{Message: fmt.Sprintf("%s over budget %s", Pct(r.Change), r.Budget), Type: "regression", Text: detail}
		case verdict.Inconclusive:
			suite.Skipped++
			tc.Skipped = &skipped{Message: "inconclusive: " + detail}
		}
		suite.Cases = append(suite.Cases, tc)
	}
	suite.Tests = len(suite.Cases)
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(testsuites{Name: "jvq", Suites: []testsuite{suite}}); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}
