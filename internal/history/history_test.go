// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package history

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/just-very-quickly/jvq/internal/verdict"
)

func f(v float64) *float64 { return &v }

func row(name string, change float64, v string) verdict.Row {
	return verdict.Row{Name: name, Metric: "time", Unit: "ns/op",
		Base: &verdict.Side{N: 10, Median: 100}, Head: &verdict.Side{N: 10, Median: 100 * (1 + change)},
		Change: f(change), Verdict: v}
}

func TestAppendReadAndTrends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	when := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	// Ten runs that each make A 1% slower, all within a 5% budget, and B
	// wobble around nothing.
	for i := 0; i < 10; i++ {
		b := 0.01
		if i%2 == 1 {
			b = -0.01
		}
		rows := []verdict.Row{row("A", 0.01, verdict.Pass), row("B", b, verdict.Pass)}
		if i == 9 {
			rows = append(rows, verdict.Row{Name: "New", Metric: "time", Head: &verdict.Side{N: 1, Median: 5}, Verdict: verdict.New})
		}
		prev := "base"
		if i > 0 {
			prev = strings.Repeat(string(rune('a'+i-1)), 40)
		}
		e := FromRows("0.5.0", when.Add(time.Duration(i)*time.Hour), Commit{SHA: prev}, Commit{Ref: "HEAD", SHA: strings.Repeat(string(rune('a'+i)), 40)}, verdict.Pass, 0.95, rows)
		if err := Append(path, e, 0); err != nil {
			t.Fatal(err)
		}
	}
	fh, _ := os.Open(path)
	entries, bad, err := Read(fh)
	fh.Close()
	if err != nil || bad != 0 || len(entries) != 10 {
		t.Fatalf("read %d entries, %d bad, %v", len(entries), bad, err)
	}
	if entries[0].Date != "2026-10-19T08:00:00Z" || entries[0].JVQ != "0.5.0" {
		t.Errorf("entry %+v", entries[0])
	}
	trends := Trends(entries)
	if len(trends) != 2 || trends[0].Name != "A" {
		t.Fatalf("trends %+v", trends)
	}
	if want := math.Pow(1.01, 10) - 1; math.Abs(trends[0].Compounded-want) > 1e-12 {
		t.Errorf("A moved %v in all, want %v", trends[0].Compounded, want)
	}
	if math.Abs(trends[1].Compounded) > 0.001 || len(trends[1].Changes) != 10 {
		t.Errorf("B %+v", trends[1])
	}
	if g := Gaps(entries); g != 0 {
		t.Errorf("%d gaps in an unbroken chain", g)
	}
}

func TestKeepAndDamagedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.jsonl")
	os.WriteFile(path, []byte("not json\n{\"head\": {}}\n"), 0o644)
	for i := 0; i < 5; i++ {
		if err := Append(path, Entry{Head: Commit{SHA: string(rune('a' + i))}}, 3); err != nil {
			t.Fatal(err)
		}
	}
	fh, _ := os.Open(path)
	entries, bad, _ := Read(fh)
	fh.Close()
	if len(entries) != 3 || bad != 0 || entries[0].Head.SHA != "c" {
		t.Errorf("kept %d, bad %d, first %+v", len(entries), bad, entries)
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".h.jsonl.tmp-*"))
	if len(matches) != 0 {
		t.Errorf("temporary files left: %v", matches)
	}
	fh, _ = os.Open(path)
	defer fh.Close()
	os.WriteFile(path+"2", []byte("garbage\n{\"head\": {\"sha\": \"x\"}}\n"), 0o644)
	g, _ := os.Open(path + "2")
	defer g.Close()
	if entries, bad, _ := Read(g); len(entries) != 1 || bad != 1 {
		t.Errorf("damaged: %d entries, %d bad", len(entries), bad)
	}
}

func TestSpark(t *testing.T) {
	if got := Spark([]float64{0, 0.01, -0.02, 0.04}); got != "·▃▁█" {
		t.Errorf("spark %q", got)
	}
	if got := Spark([]float64{0, 0}); got != "··" {
		t.Errorf("flat %q", got)
	}
}

func TestIntervalsChainAndLimit(t *testing.T) {
	// Twelve runs with no real change, each a noisy estimate (an interval
	// of about ±6%): the chained point estimate wanders, but its interval
	// is wide enough that a 10% limit isn't breached.
	var entries []Entry
	changes := []float64{0.03, -0.02, 0.05, 0.04, -0.01, 0.06, 0.02, 0.03, -0.03, 0.04, 0.02, 0.01}
	for i, c := range changes {
		r := Result{Name: "A", Metric: "time", Change: f(c), Lo: f(c - 0.06), Hi: f(c + 0.06), Verdict: verdict.Inconclusive}
		entries = append(entries, Entry{Base: Commit{SHA: string(rune('a' + i))}, Head: Commit{SHA: string(rune('b' + i))}, Confidence: 0.95, Results: []Result{r}})
	}
	tr := Trends(entries)[0]
	if tr.Compounded < 0.2 || !tr.HasInterval || tr.Lo > 0.1 || tr.Hi < tr.Compounded {
		t.Errorf("trend %+v", tr)
	}
	if tr.Over(0.10) {
		t.Errorf("noise alone went over the limit: %+v", tr)
	}
	// The same chain with tight intervals: a real creep, over the limit.
	for i := range entries {
		c := *entries[i].Results[0].Change
		entries[i].Results[0].Lo, entries[i].Results[0].Hi = f(c-0.002), f(c+0.002)
	}
	if tr := Trends(entries)[0]; !tr.Over(0.10) {
		t.Errorf("a clear creep wasn't over: %+v", tr)
	}
	// Results no run gated never count.
	for i := range entries {
		entries[i].Results[0].Verdict = verdict.Info
	}
	if tr := Trends(entries)[0]; tr.Over(0.10) {
		t.Error("an ungated result counted")
	}
	// One run gives back its own interval, lopsided as rank intervals are.
	one := []Entry{{Head: Commit{SHA: "x"}, Confidence: 0.95, Results: []Result{{Name: "A", Metric: "time", Change: f(-0.056), Lo: f(-0.217), Hi: f(0.504), Verdict: verdict.Inconclusive}}}}
	if tr := Trends(one)[0]; math.Abs(tr.Lo+0.217) > 1e-9 || math.Abs(tr.Hi-0.504) > 1e-9 {
		t.Errorf("one run: %v … %v, want -0.217 … 0.504", tr.Lo, tr.Hi)
	}
	// A time result with no interval leaves the trend without one.
	entries[0].Results[0].Lo = nil
	if tr := Trends(entries)[0]; tr.HasInterval {
		t.Error("interval without every run's")
	}
}

func TestReRunReplacesItsLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.jsonl")
	os.WriteFile(path, nil, 0o640)
	e := Entry{Base: Commit{SHA: "x"}, Head: Commit{SHA: "y"}, Verdict: "pass"}
	Append(path, e, 0)
	e.Verdict = "regression"
	Append(path, e, 0)
	fh, _ := os.Open(path)
	entries, _, _ := Read(fh)
	fh.Close()
	if len(entries) != 1 || entries[0].Verdict != "regression" {
		t.Errorf("entries %+v", entries)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if err := Append(filepath.Join(t.TempDir(), "no", "h.jsonl"), e, 0); err == nil || !strings.Contains(err.Error(), "doesn't exist") {
		t.Errorf("missing folder: %v", err)
	}
	if last, ok := Last(path); !ok || last.Head.SHA != "y" {
		t.Errorf("last %+v", last)
	}
}
