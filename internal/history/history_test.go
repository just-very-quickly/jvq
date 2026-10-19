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
		e := FromRows("0.5.0", when.Add(time.Duration(i)*time.Hour), Commit{SHA: "base"}, Commit{Ref: "HEAD", SHA: strings.Repeat(string(rune('a'+i)), 40)}, verdict.Pass, rows)
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
	if trends[0].First != strings.Repeat("a", 40) || trends[0].Last != strings.Repeat("j", 40) {
		t.Errorf("first and last %s %s", trends[0].First, trends[0].Last)
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
