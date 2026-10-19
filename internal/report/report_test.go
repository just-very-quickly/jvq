// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"bytes"
	"encoding/json"
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
