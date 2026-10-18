// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package stats

import (
	"math"
	"math/rand"
	"testing"
)

func TestMedian(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{
		{[]float64{3}, 3},
		{[]float64{3, 1, 2}, 2},
		{[]float64{4, 1, 3, 2}, 2.5},
		{[]float64{5, 5, 5, 1}, 5},
	}
	for _, c := range cases {
		in := append([]float64(nil), c.in...)
		if got := Median(c.in); got != c.want {
			t.Errorf("Median(%v) = %v, want %v", c.in, got, c.want)
		}
		for i := range in {
			if in[i] != c.in[i] {
				t.Fatalf("Median changed its input")
			}
		}
	}
	if !math.IsNaN(Median(nil)) {
		t.Error("Median(nil) should be NaN")
	}
}

// The exact critical values of the two-sided Mann-Whitney test at 0.05
// (published tables, e.g. Siegel & Castellan): the CI starts at d(Ucrit+1).
func TestExactKMatchesTables(t *testing.T) {
	cases := []struct{ n, m, ucrit int }{
		{5, 5, 2}, {6, 6, 5}, {8, 8, 13}, {10, 10, 23}, {12, 12, 37},
		{15, 15, 64}, {20, 20, 127}, {5, 10, 8}, {8, 12, 22},
	}
	for _, c := range cases {
		if k := exactK(c.n, c.m, 0.05); k != c.ucrit+1 {
			t.Errorf("n=%d m=%d: k=%d, want %d", c.n, c.m, k, c.ucrit+1)
		}
	}
}

func TestUCDFIsADistribution(t *testing.T) {
	for _, nm := range [][2]int{{1, 1}, {3, 4}, {7, 7}, {10, 12}} {
		cdf := uCDF(nm[0], nm[1])
		if len(cdf) != nm[0]*nm[1]+1 {
			t.Fatalf("%v: %d entries", nm, len(cdf))
		}
		if math.Abs(cdf[len(cdf)-1]-1) > 1e-12 {
			t.Errorf("%v: total %v", nm, cdf[len(cdf)-1])
		}
		// U is symmetric around n*m/2.
		N := nm[0] * nm[1]
		for u := 0; u < N; u++ {
			pu := cdf[u]
			if u > 0 {
				pu -= cdf[u-1]
			}
			v := N - u
			pv := cdf[v]
			if v > 0 {
				pv -= cdf[v-1]
			}
			if math.Abs(pu-pv) > 1e-12 {
				t.Fatalf("%v: P(U=%d)=%v but P(U=%d)=%v", nm, u, pu, v, pv)
			}
		}
	}
	// n=m=2: arrangements of xxyy: U takes 0,1,2,2,3,4 each 1/6.
	cdf := uCDF(2, 2)
	want := []float64{1.0 / 6, 2.0 / 6, 4.0 / 6, 5.0 / 6, 1}
	for i := range want {
		if math.Abs(cdf[i]-want[i]) > 1e-12 {
			t.Errorf("uCDF(2,2)[%d] = %v, want %v", i, cdf[i], want[i])
		}
	}
}

func TestHodgesLehmannKnownShift(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	ys := make([]float64, len(xs))
	for i, x := range xs {
		ys[i] = x + 3
	}
	sh, err := HodgesLehmann(xs, ys, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	if sh.Estimate != 3 {
		t.Errorf("estimate %v, want 3", sh.Estimate)
	}
	if !sh.Bounded || !sh.Exact || !(sh.Lo <= 3 && 3 <= sh.Hi) {
		t.Errorf("interval %+v should hold 3", sh)
	}
}

func TestHodgesLehmannIgnoresOneOutlier(t *testing.T) {
	xs := []float64{10, 10.1, 9.9, 10.05, 9.95, 10.02, 9.98, 10.01}
	ys := []float64{10, 10.1, 9.9, 10.05, 9.95, 10.02, 9.98, 1000}
	sh, _ := HodgesLehmann(xs, ys, 0.95)
	if math.Abs(sh.Estimate) > 0.1 {
		t.Errorf("one outlier moved the estimate to %v", sh.Estimate)
	}
	if !(sh.Lo <= 0 && 0 <= sh.Hi) {
		t.Errorf("interval %v..%v should hold 0", sh.Lo, sh.Hi)
	}
}

func TestTooSmallForAnInterval(t *testing.T) {
	sh, err := HodgesLehmann([]float64{1, 2}, []float64{3, 4}, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	if sh.Bounded || !math.IsInf(sh.Lo, -1) || !math.IsInf(sh.Hi, 1) {
		t.Errorf("2x2 samples can't give a 95%% interval, got %+v", sh)
	}
	if _, err := HodgesLehmann(nil, []float64{1}, 0.95); err != ErrTooFew {
		t.Errorf("empty sample: %v", err)
	}
	if _, err := HodgesLehmann([]float64{1}, []float64{1}, 1.5); err == nil {
		t.Error("level 1.5 accepted")
	}
}

// The interval's coverage, checked by simulation: for a known shift in skewed
// noise, the 95% interval should hold the true shift about 95% of the time
// (the Moses interval is conservative, so at least that often).
func TestCoverage(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	const trials = 2000
	const shift = 0.7
	hold := 0
	for i := 0; i < trials; i++ {
		xs := make([]float64, 10)
		ys := make([]float64, 10)
		for j := range xs {
			xs[j] = r.ExpFloat64()
			ys[j] = r.ExpFloat64() + shift
		}
		sh, _ := HodgesLehmann(xs, ys, 0.95)
		if sh.Lo <= shift && shift <= sh.Hi {
			hold++
		}
	}
	cov := float64(hold) / trials
	if cov < 0.935 || cov > 0.985 {
		t.Errorf("coverage %.3f, want about 0.95", cov)
	}
}

func TestNormalApproximationForLargeSamples(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	xs := make([]float64, 60)
	ys := make([]float64, 60)
	for i := range xs {
		xs[i] = r.NormFloat64()
		ys[i] = r.NormFloat64() + 1
	}
	sh, _ := HodgesLehmann(xs, ys, 0.95)
	if sh.Exact {
		t.Error("60x60 should use the normal approximation")
	}
	if !sh.Bounded || !(sh.Lo < 1 && 1 < sh.Hi) {
		t.Errorf("interval %v..%v should hold 1", sh.Lo, sh.Hi)
	}
	// At the boundary the exact and the approximate k agree within one rank.
	for _, n := range []int{20, 30, 40, 50} {
		exact := exactK(n, n, 0.05)
		z := normalQuantile(0.975)
		sd := math.Sqrt(float64(n*n) * float64(2*n+1) / 12)
		approx := int(math.Floor(float64(n*n)/2 - z*sd))
		if d := exact - approx; d < -1 || d > 1 {
			t.Errorf("n=%d: exact k %d, approximate %d", n, exact, approx)
		}
	}
}

func TestMannWhitneyP(t *testing.T) {
	same := []float64{1, 2, 3, 4, 5}
	if p := MannWhitneyP(same, same); p < 0.9 {
		t.Errorf("identical samples p=%v", p)
	}
	lo := []float64{1, 2, 3, 4, 5, 6, 7, 8}
	hi := []float64{11, 12, 13, 14, 15, 16, 17, 18}
	if p := MannWhitneyP(lo, hi); p > 0.002 {
		t.Errorf("separated samples p=%v", p)
	}
	if p := MannWhitneyP([]float64{2, 2, 2}, []float64{2, 2}); p != 1 {
		t.Errorf("all tied p=%v", p)
	}
	// scipy.stats.mannwhitneyu([1,3,5,7,9],[2,4,6,8,10,12],
	//   use_continuity=True, alternative='two-sided', method='asymptotic')
	// gives p = 0.41131 (checked against scipy 1.x).
	if p := MannWhitneyP([]float64{1, 3, 5, 7, 9}, []float64{2, 4, 6, 8, 10, 12}); math.Abs(p-0.41131) > 0.0001 {
		t.Errorf("p=%v, want 0.41131", p)
	}
}

// jvq gates its own speed with these, in .github/workflows/benchmarks.yml.
func BenchmarkHodgesLehmann10(b *testing.B) { benchHL(b, 10) }
func BenchmarkHodgesLehmann30(b *testing.B) { benchHL(b, 30) }

func benchHL(b *testing.B, n int) {
	r := rand.New(rand.NewSource(1))
	xs := make([]float64, n)
	ys := make([]float64, n)
	for i := range xs {
		xs[i] = r.NormFloat64()
		ys[i] = r.NormFloat64() + 0.1
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := HodgesLehmann(xs, ys, 0.95); err != nil {
			b.Fatal(err)
		}
	}
}
