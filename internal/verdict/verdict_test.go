// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package verdict

import (
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/just-very-quickly/jvq/internal/adapter"
	"github.com/just-very-quickly/jvq/internal/config"
)

func timeSamples(name string, center float64, noise float64, n int, r *rand.Rand) []adapter.Sample {
	out := make([]adapter.Sample, n)
	for i := range out {
		v := center * (1 + noise*r.NormFloat64())
		out[i] = adapter.Sample{Package: "p", Name: name, Metric: adapter.Time, Unit: "ns/op", Value: v}
	}
	return out
}

func one(t *testing.T, rows []Row, name, metric string) Row {
	t.Helper()
	for _, r := range rows {
		if r.Name == name && r.Metric == metric {
			return r
		}
	}
	t.Fatalf("no row for %s %s in %+v", name, metric, rows)
	return Row{}
}

func TestTimeVerdicts(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	cfg := config.Default()
	cases := []struct {
		name  string
		ratio float64
		noise float64
		want  string
	}{
		{"BenchmarkSame", 1.00, 0.005, Pass},
		{"BenchmarkSlower", 1.30, 0.005, Regression},
		{"BenchmarkFaster", 0.70, 0.005, Improved},
		{"BenchmarkNoisy", 1.05, 0.20, Inconclusive},
		{"BenchmarkJustUnder", 1.02, 0.002, Pass},
	}
	for _, c := range cases {
		base := timeSamples(c.name, 1000, c.noise, 10, r)
		head := timeSamples(c.name, 1000*c.ratio, c.noise, 10, r)
		row := one(t, Compare(base, head, cfg), c.name, adapter.Time)
		if row.Verdict != c.want {
			t.Errorf("%s: verdict %s, want %s (row %+v, ci %v..%v)", c.name, row.Verdict, c.want, row, *row.Lo, *row.Hi)
		}
		if c.noise < 0.05 && math.Abs(*row.Change-(c.ratio-1)) > 0.01 {
			t.Errorf("%s: change %v, want about %v", c.name, *row.Change, c.ratio-1)
		}
		if row.Budget != "5%" || row.Base.N != 10 || row.Head.N != 10 {
			t.Errorf("%s: row %+v", c.name, row)
		}
	}
}

func TestBudgetOffAndOverride(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	cfg, err := config.Parse("[budget]\ntime = \"off\"\n[budget.bench.\"BenchmarkStrict\"]\ntime = \"1%\"\n")
	if err != nil {
		t.Fatal(err)
	}
	base := append(timeSamples("BenchmarkLoose", 100, 0.001, 10, r), timeSamples("BenchmarkStrict", 100, 0.001, 10, r)...)
	head := append(timeSamples("BenchmarkLoose", 150, 0.001, 10, r), timeSamples("BenchmarkStrict", 103, 0.001, 10, r)...)
	rows := Compare(base, head, cfg)
	if v := one(t, rows, "BenchmarkLoose", adapter.Time).Verdict; v != Info {
		t.Errorf("budget off: %s", v)
	}
	// Off wins even when the samples are too few to judge.
	few := Compare(timeSamples("BenchmarkLoose", 100, 0.001, 2, r), timeSamples("BenchmarkLoose", 150, 0.001, 2, r), cfg)
	if v := one(t, few, "BenchmarkLoose", adapter.Time).Verdict; v != Info {
		t.Errorf("budget off, two samples: %s", v)
	}
	if v := one(t, rows, "BenchmarkStrict", adapter.Time).Verdict; v != Regression {
		t.Errorf("1%% budget, 3%% slower: %s", v)
	}
}

func TestTooFewSamples(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	row := one(t, Compare(timeSamples("B", 1, 0.01, 2, r), timeSamples("B", 2, 0.01, 2, r), config.Default()), "B", adapter.Time)
	if row.Verdict != Inconclusive || !strings.Contains(row.Note, "too few") {
		t.Errorf("%+v", row)
	}
	// Three a side is enough to run, but no 95% interval exists at that size.
	row = one(t, Compare(timeSamples("B", 1, 0.01, 3, r), timeSamples("B", 2, 0.01, 3, r), config.Default()), "B", adapter.Time)
	if row.Verdict != Inconclusive || row.Change == nil {
		t.Errorf("3x3: %+v", row)
	}
}

func TestZeroTime(t *testing.T) {
	base := []adapter.Sample{{Name: "B", Metric: adapter.Time, Value: 0}, {Name: "B", Metric: adapter.Time, Value: 1}, {Name: "B", Metric: adapter.Time, Value: 1}}
	row := one(t, Compare(base, base, config.Default()), "B", adapter.Time)
	if row.Verdict != Inconclusive || !strings.Contains(row.Note, "zero") {
		t.Errorf("%+v", row)
	}
}

func exact(name, metric string, vals ...float64) []adapter.Sample {
	var out []adapter.Sample
	for _, v := range vals {
		out = append(out, adapter.Sample{Name: name, Metric: metric, Unit: metric + "/op", Value: v})
	}
	return out
}

func TestAllocsAndBytes(t *testing.T) {
	cfg, _ := config.Parse("[budget]\nallocs = \"0\"\nbytes = \"10%\"\n")
	base := append(exact("B", adapter.Allocs, 3, 3, 3, 3), exact("B", adapter.Bytes, 100, 100, 100)...)
	cases := []struct {
		metric string
		head   []float64
		want   string
	}{
		{adapter.Allocs, []float64{3, 3, 3, 3}, Pass},
		{adapter.Allocs, []float64{4, 4, 4, 3}, Regression},
		{adapter.Allocs, []float64{3, 4, 3, 3}, Pass}, // median 3: one odd sample doesn't count
		{adapter.Allocs, []float64{2, 2, 2, 2}, Improved},
		{adapter.Bytes, []float64{110, 110, 110}, Pass},
		{adapter.Bytes, []float64{111, 111, 111}, Regression},
	}
	for _, c := range cases {
		head := exact("B", c.metric, c.head...)
		row := one(t, Compare(base, head, cfg), "B", c.metric)
		if row.Verdict != c.want {
			t.Errorf("%s %v: %s, want %s (%+v)", c.metric, c.head, row.Verdict, c.want, row)
		}
	}
	// From zero allocations to one.
	row := one(t, Compare(exact("Z", adapter.Allocs, 0, 0, 0), exact("Z", adapter.Allocs, 1, 1, 1), cfg), "Z", adapter.Allocs)
	if row.Verdict != Regression || !math.IsInf(*row.Change, 1) {
		t.Errorf("0 -> 1 alloc: %+v", row)
	}
	row = one(t, Compare(exact("Z", adapter.Allocs, 0, 0, 0), exact("Z", adapter.Allocs, 0, 0, 0), cfg), "Z", adapter.Allocs)
	if row.Verdict != Pass || *row.Change != 0 {
		t.Errorf("0 -> 0: %+v", row)
	}
	// Off by default.
	row = one(t, Compare(exact("B", adapter.Allocs, 1), exact("B", adapter.Allocs, 9), config.Default()), "B", adapter.Allocs)
	if row.Verdict != Info {
		t.Errorf("allocs gated by default: %+v", row)
	}
}

// The note on an exact regression is in the metric's own unit, as in the
// table: a size in MB, not 6.44254e+06.
func TestExactNoteUnits(t *testing.T) {
	cfg, _ := config.Parse("[budget]\nallocs = \"0\"\nbytes = \"0\"\n")
	cases := []struct {
		metric, unit string
		base, head   float64
		want         string
	}{
		{adapter.Allocs, "allocs/op", 0, 2, "+2 over the limit of 0"},
		{adapter.Bytes, "B/op", 0, 104, "+104 B over the limit of 0 B"},
		{adapter.Size, "B", 2284431, 8726940, "+6.443 MB over the limit of 2.307 MB"},
		{adapter.Size, "B", 900, 1200, "+300 B over the limit of 909 B"},
	}
	for _, c := range cases {
		base := []adapter.Sample{{Name: "x", Metric: c.metric, Unit: c.unit, Value: c.base}}
		head := []adapter.Sample{{Name: "x", Metric: c.metric, Unit: c.unit, Value: c.head}}
		row := one(t, Compare(base, head, cfg), "x", c.metric)
		if row.Verdict != Regression || row.Note != c.want {
			t.Errorf("%s %v -> %v: %s %q, want %q", c.metric, c.base, c.head, row.Verdict, row.Note, c.want)
		}
	}
}

func TestNewRemovedCustomIgnored(t *testing.T) {
	cfg, _ := config.Parse("[ignore]\nbench = [\"BenchmarkSkip*\"]\n")
	base := append(exact("BenchmarkOld", adapter.Time, 1, 1, 1), exact("BenchmarkSkipMe", adapter.Time, 1, 1, 1)...)
	base = append(base, exact("BenchmarkX", "MB/s", 100)...)
	head := append(exact("BenchmarkNew", adapter.Time, 1, 1, 1), exact("BenchmarkSkipMe", adapter.Time, 9, 9, 9)...)
	head = append(head, exact("BenchmarkX", "MB/s", 50)...)
	rows := Compare(base, head, cfg)
	if v := one(t, rows, "BenchmarkOld", adapter.Time).Verdict; v != Removed {
		t.Errorf("old: %s", v)
	}
	if v := one(t, rows, "BenchmarkNew", adapter.Time).Verdict; v != New {
		t.Errorf("new: %s", v)
	}
	if row := one(t, rows, "BenchmarkX", "MB/s"); row.Verdict != Info || *row.Change != -0.5 {
		t.Errorf("custom: %+v", row)
	}
	for _, r := range rows {
		if strings.HasPrefix(r.Name, "BenchmarkSkip") {
			t.Errorf("ignored benchmark reported: %+v", r)
		}
	}
	// A run where nothing is gated can't pass.
	if o := Overall(rows); o != Inconclusive {
		t.Errorf("overall %s", o)
	}
}

func TestOverallAndOrder(t *testing.T) {
	rows := []Row{{Verdict: Pass}, {Verdict: Info}, {Verdict: New}}
	if Overall(rows) != Pass {
		t.Error("pass")
	}
	rows = append(rows, Row{Verdict: Inconclusive})
	if Overall(rows) != Inconclusive {
		t.Error("inconclusive")
	}
	rows = append(rows, Row{Verdict: Regression})
	if Overall(rows) != Regression {
		t.Error("regression")
	}
	if c := Counts(rows); c[Pass] != 1 || c[Regression] != 1 {
		t.Errorf("counts %v", c)
	}
	base := append(exact("B", "MB/s", 1), exact("B", adapter.Allocs, 1)...)
	base = append(base, exact("A", adapter.Time, 1, 1, 1)...)
	base = append(base, exact("B", adapter.Time, 1, 1, 1)...)
	got := Compare(base, base, config.Default())
	order := []string{}
	for _, r := range got {
		order = append(order, r.Name+":"+r.Metric)
	}
	want := "A:time B:time B:allocs B:MB/s"
	if strings.Join(order, " ") != want {
		t.Errorf("order %v, want %s", order, want)
	}
}

// With no real change and the default settings, regressions should almost
// never be called: the whole 95% interval would have to sit above +5%.
func TestFalseAlarmRateOnSimulatedNoise(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	cfg := config.Default()
	regressions := 0
	const trials = 2000
	for i := 0; i < trials; i++ {
		// Skewed noise: lognormal with 3% spread plus occasional 30% spikes.
		gen := func() []adapter.Sample {
			s := make([]adapter.Sample, 10)
			for j := range s {
				v := 1000 * math.Exp(0.03*r.NormFloat64())
				if r.Float64() < 0.05 {
					v *= 1.3
				}
				s[j] = adapter.Sample{Name: "B", Metric: adapter.Time, Value: v}
			}
			return s
		}
		if one(t, Compare(gen(), gen(), cfg), "B", adapter.Time).Verdict == Regression {
			regressions++
		}
	}
	if regressions > trials/200 {
		t.Errorf("%d false regressions in %d no-change trials", regressions, trials)
	}
}

func TestNeedsMore(t *testing.T) {
	cases := []struct {
		rows []Row
		want bool
	}{
		{[]Row{{Metric: adapter.Time, Verdict: Inconclusive, Note: Straddles}}, true},
		{[]Row{{Metric: adapter.Time, Verdict: Inconclusive, Note: "too few samples (need 3 a side)"}}, true},
		{[]Row{{Metric: adapter.Time, Verdict: Inconclusive, Note: "a time of zero or less can't be compared as a ratio"}}, false},
		{[]Row{{Metric: adapter.Time, Verdict: Pass}, {Metric: adapter.Time, Verdict: Regression}}, false},
		{[]Row{{Metric: adapter.Allocs, Verdict: Inconclusive, Note: Straddles}}, false},
		{nil, false},
	}
	for i, c := range cases {
		if got := NeedsMore(c.rows); got != c.want {
			t.Errorf("case %d: %v", i, got)
		}
	}
}

func TestUngatedNote(t *testing.T) {
	rows := []Row{{Name: "a", Metric: "seconds", Unit: "seconds", Verdict: Info}}
	if n := UngatedNote(rows); !strings.Contains(n, `"seconds"`) {
		t.Errorf("note %q", n)
	}
	rows = append(rows, Row{Name: "b", Metric: "time", Unit: "ns", Verdict: Pass})
	if n := UngatedNote(rows); n != "" {
		t.Errorf("note on a gated run: %q", n)
	}
	if n := UngatedNote([]Row{{Name: "b", Metric: "time", Verdict: Info}}); !strings.Contains(n, "budget is off") {
		t.Errorf("note %q", n)
	}
}
