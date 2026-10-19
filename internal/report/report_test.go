// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"math"
	"strings"
	"testing"

	"github.com/just-very-quickly/jvq/internal/verdict"
)

func f(v float64) *float64 { return &v }

func sample() Report {
	return Report{
		JVQ: "0.1.0", Verdict: verdict.Regression,
		Base: Commit{Ref: "merge-base(origin/main, head)", SHA: "aaaaaaaaaaaaaaaa"},
		Head: Commit{Ref: "HEAD", SHA: "bbbbbbbbbbbbbbbb"},
		Note: "head is on main", Command: "go test -bench=.", Adapter: "go",
		Rounds: 10, Warmup: true, Confidence: 0.95, Seconds: 12.34,
		Counts: map[string]int{verdict.Regression: 1, verdict.Pass: 1, verdict.Info: 1},
		Rows: []verdict.Row{
			{Package: "a", Name: "BenchmarkParse|x", Metric: "time", Unit: "ns/op",
				Base: &verdict.Side{N: 10, Median: 1500}, Head: &verdict.Side{N: 10, Median: 1800},
				Change: f(0.2), Lo: f(0.18), Hi: f(0.22), Budget: "5%", Verdict: verdict.Regression},
			{Package: "b", Name: "BenchmarkParse|x", Metric: "allocs", Unit: "allocs/op",
				Base: &verdict.Side{N: 10, Median: 0}, Head: &verdict.Side{N: 10, Median: 1},
				Change: f(math.Inf(1)), Budget: "0", Verdict: verdict.Regression, Note: "+1 over the limit of 0"},
			{Name: "BenchmarkOK", Metric: "time", Unit: "ns/op",
				Base: &verdict.Side{N: 10, Median: 2.5e6}, Head: &verdict.Side{N: 10, Median: 2.5e6},
				Change: f(0), Lo: f(-0.01), Hi: f(0.01), Budget: "5%", Verdict: verdict.Pass},
			{Name: "BenchmarkNew", Metric: "MB/s", Unit: "MB/s", Head: &verdict.Side{N: 3, Median: 12.5},
				Budget: "off", Verdict: verdict.New},
		},
	}
}

func TestText(t *testing.T) {
	var b bytes.Buffer
	Text(&b, sample())
	out := b.String()
	for _, want := range []string{
		"base aaaaaaaaaa (merge-base(origin/main, head)) vs head bbbbbbbbbb (HEAD), 10 rounds, 95% intervals",
		"note: head is on main",
		"a.BenchmarkParse|x", "b.BenchmarkParse|x", // same name in two packages
		"1.5 µs", "1.8 µs", "+20.0%", "+18.0% … +22.0%", "REGRESSION",
		"2.5 ms", "0.0%",
		"verdict: REGRESSION (1 regression, 1 pass, 1 info) in 12.3s",
		"b.BenchmarkParse|x allocs: +1 over the limit of 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "a.BenchmarkOK") {
		t.Error("package shown where names don't collide")
	}
}

func TestUndecidedAtTheLimit(t *testing.T) {
	rep := sample()
	rep.Verdict, rep.Rounds, rep.MaxRounds = verdict.Inconclusive, 30, 30
	var b bytes.Buffer
	Text(&b, rep)
	if !strings.Contains(b.String(), "still undecided at the 30-round limit") {
		t.Errorf("no limit note:\n%s", b.String())
	}
}

func TestMarkdown(t *testing.T) {
	var b bytes.Buffer
	Markdown(&b, sample())
	out := b.String()
	for _, want := range []string{
		"### jvq: regression",
		"Base `aaaaaaaaaa` (merge-base(origin/main, head)) against head `bbbbbbbbbb` (HEAD), 10 interleaved rounds, 95% intervals. 1 regression, 1 pass, 1 info.",
		"_head is on main._",
		`| a.BenchmarkParse\|x | time | 1.5 µs | 1.8 µs | +20.0% | +18.0% … +22.0% | 5% | **regression** |`,
		"| BenchmarkNew | MB/s | - | 12.5 MB/s |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown lacks %q:\n%s", want, out)
		}
	}
}

func TestJSON(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, sample()); err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b.Bytes(), &back); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, b.String())
	}
	if back["verdict"] != "regression" || back["jvq"] != "0.1.0" {
		t.Errorf("top level %v", back)
	}
	rows := back["results"].([]any)
	r0 := rows[0].(map[string]any)
	if r0["ci_lo"] != 0.18 || r0["change"] != 0.2 || r0["package"] != "a" {
		t.Errorf("row 0 %v", r0)
	}
	r1 := rows[1].(map[string]any)
	if _, has := r1["change"]; has {
		t.Errorf("infinite change should be left out: %v", r1)
	}
}

func TestFormatting(t *testing.T) {
	cases := map[string]string{
		Duration(12):                   "12 ns",
		Duration(1234):                 "1.23 µs",
		Duration(12.5e6):               "12.5 ms",
		Duration(3e9):                  "3 s",
		Value("bytes", "", 64):         "64 B",
		Value("allocs", "", 2):         "2",
		Value("MB/s", "MB/s", 123.456): "123.5 MB/s",
		Pct(f(0.0723)):                 "+7.2%",
		Pct(f(-0.5)):                   "-50.0%",
		Pct(f(0.0001)):                 "0.0%",
		Pct(nil):                       "",
		Interval(nil, f(1)):            "",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if md("a|b_c*d`e\nf") != `a\|b\_c\*d'e f` {
		t.Errorf("md escaping: %q", md("a|b_c*d`e\nf"))
	}
	if summary(nil) != "no results" || capitalise("") != "" {
		t.Error("empty summary")
	}
}

func TestCompareMode(t *testing.T) {
	rep := sample()
	rep.Mode = ModeCompare
	rep.Base, rep.Head = Commit{Ref: "old.txt"}, Commit{Ref: "new `x`.txt"}
	var b bytes.Buffer
	Text(&b, rep)
	if !strings.Contains(b.String(), "old old.txt vs new new `x`.txt, 95% intervals") || strings.Contains(b.String(), "rounds") {
		t.Errorf("text:\n%s", b.String())
	}
	b.Reset()
	Markdown(&b, rep)
	if !strings.Contains(b.String(), "Old `old.txt` against new `new 'x'.txt`, 95% intervals.") {
		t.Errorf("markdown:\n%s", b.String())
	}
}

func TestSizeFormatting(t *testing.T) {
	for v, want := range map[float64]string{512: "512 B", 9999: "9999 B", 12345: "12.35 kB", 2.5e6: "2.5 MB", 3.25e9: "3.25 GB"} {
		if got := Value("size", "B", v); got != want {
			t.Errorf("%v: %q, want %q", v, got, want)
		}
	}
}

func TestJUnit(t *testing.T) {
	rep := sample()
	rep.Rows = append(rep.Rows, verdict.Row{Name: "BenchmarkMaybe", Metric: "time", Unit: "ns/op",
		Base: &verdict.Side{N: 10, Median: 100}, Head: &verdict.Side{N: 10, Median: 104},
		Change: f(0.04), Lo: f(-0.01), Hi: f(0.09), Budget: "5%", Verdict: verdict.Inconclusive, Note: verdict.Straddles})
	var b bytes.Buffer
	if err := JUnit(&b, rep); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`<testsuite name="jvq" tests="4" failures="2" errors="0" skipped="1" time="12.340">`,
		`<testcase classname="jvq.a" name="a.BenchmarkParse|x time" time="0">`,
		`<failure message="+20.0% over budget 5%" type="regression">`,
		`<skipped message="inconclusive: base 100 ns, head 104 ns, change +4.0%, interval -1.0% … +9.0%, budget 5%; the interval straddles the budget">`,
		`name="BenchmarkOK time"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("JUnit lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "BenchmarkNew") {
		t.Error("a new benchmark, which isn't gated, became a test case")
	}
	var v struct{}
	if err := xml.Unmarshal(b.Bytes(), &v); err != nil {
		t.Errorf("not valid XML: %v", err)
	}
}

func TestCount(t *testing.T) {
	for n, want := range map[float64]string{13438495: "13.44 M", 2.5e9: "2.5 G", 51234: "51.23 k", 812: "812"} {
		if got := Count(n); got != want {
			t.Errorf("Count(%v) = %s, want %s", n, got, want)
		}
	}
}
