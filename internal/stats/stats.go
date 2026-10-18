// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package stats holds the rank statistics jvq decides with: the median, the
// Mann-Whitney U test, and the Hodges-Lehmann estimate of the shift between
// two samples with its distribution-free (Moses) confidence interval.
//
// Nothing here assumes the samples are normally distributed. Benchmark
// timings aren't: they have long right tails from interrupts, scheduling and
// cache misses. Rank methods only care about the order of the values, so one
// wild outlier moves the answer by one rank, not by its size.
package stats

import (
	"errors"
	"math"
	"sort"
)

// Median returns the median of xs, or NaN when xs is empty. xs is not
// changed.
func Median(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := sorted(xs)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func sorted(xs []float64) []float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s
}

// Shift is the Hodges-Lehmann estimate of how far ys sits from xs (a value
// added to xs to get ys), with a confidence interval for it.
type Shift struct {
	Estimate float64 // median of all pairwise differences ys[j]-xs[i]
	Lo, Hi   float64 // the confidence interval for the shift
	Level    float64 // the interval's confidence level, such as 0.95
	Exact    bool    // true when the interval came from the exact distribution of U
	// Bounded is false when the samples are too small for an interval at
	// this level; Lo and Hi are then -Inf and +Inf.
	Bounded bool
	P       float64 // two-sided Mann-Whitney p-value for "no shift"
}

// ErrTooFew is returned when a sample is empty.
var ErrTooFew = errors.New("stats: each sample needs at least one value")

// exactLimit is the largest n*m for which the exact distribution of U is
// computed. Above it the normal approximation is used; at that size the two
// agree to well under one rank.
const exactLimit = 2500

// HodgesLehmann estimates the shift from xs to ys and its confidence interval
// at the given level (0 < level < 1) using the Moses method: the interval's
// ends are order statistics of the pairwise differences, picked from the null
// distribution of the Mann-Whitney U statistic. It needs no assumption about
// the shape of either distribution, only that the two differ by a shift.
func HodgesLehmann(xs, ys []float64, level float64) (Shift, error) {
	n, m := len(xs), len(ys)
	if n == 0 || m == 0 {
		return Shift{}, ErrTooFew
	}
	if !(level > 0 && level < 1) {
		return Shift{}, errors.New("stats: confidence level must be between 0 and 1")
	}
	d := make([]float64, 0, n*m)
	for _, y := range ys {
		for _, x := range xs {
			d = append(d, y-x)
		}
	}
	sort.Float64s(d)
	N := len(d)
	sh := Shift{Level: level, Estimate: medianSorted(d)}
	sh.P = MannWhitneyP(xs, ys)

	alpha := 1 - level
	var k int // the interval is [d(k), d(N-k+1)] in 1-based order statistics
	if n*m <= exactLimit {
		sh.Exact = true
		k = exactK(n, m, alpha)
	} else {
		z := normalQuantile(1 - alpha/2)
		sd := math.Sqrt(float64(n*m) * float64(n+m+1) / 12)
		k = int(math.Floor(float64(N)/2 - z*sd))
	}
	if k < 1 {
		sh.Lo, sh.Hi = math.Inf(-1), math.Inf(1)
		return sh, nil
	}
	sh.Bounded = true
	sh.Lo = d[k-1]
	sh.Hi = d[N-k]
	return sh, nil
}

func medianSorted(s []float64) float64 {
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// exactK returns the largest k with P(U <= k-1) <= alpha/2 under the null
// hypothesis, where U counts pairs with y > x among n*m pairs (no ties). The
// interval [d(k), d(N-k+1)] then covers the true shift with probability at
// least 1-alpha. It returns 0 when no such k exists.
func exactK(n, m int, alpha float64) int {
	cdf := uCDF(n, m)
	k := 0
	for u := 0; u < len(cdf); u++ {
		if cdf[u] <= alpha/2+1e-12 {
			k = u + 1
		} else {
			break
		}
	}
	return k
}

// uCDF returns P(U <= u) for u = 0..n*m under the null hypothesis, computed
// by the standard recurrence on the number of arrangements. Probabilities are
// carried as float64 fractions of the total, which stays exact enough for
// n*m up to exactLimit.
func uCDF(n, m int) []float64 {
	// f[i][j][u]: number of arrangements of i xs and j ys with U = u,
	// normalised so each layer sums to 1. Built with the recurrence
	// p(i,j,u) = i/(i+j) p(i-1,j,u) + j/(i+j) p(i,j-1,u-i).
	prev := make([][]float64, m+1) // prev[j] is the distribution for i-1
	for j := 0; j <= m; j++ {
		prev[j] = []float64{1} // i = 0: U is always 0
	}
	for i := 1; i <= n; i++ {
		cur := make([][]float64, m+1)
		cur[0] = []float64{1} // j = 0: U is always 0
		for j := 1; j <= m; j++ {
			size := i*j + 1
			p := make([]float64, size)
			a := float64(i) / float64(i+j)
			b := float64(j) / float64(i+j)
			for u, v := range prev[j] { // last element is an x: U unchanged
				p[u] += a * v
			}
			for u, v := range cur[j-1] { // last element is a y: it beats all i xs
				p[u+i] += b * v
			}
			cur[j] = p
		}
		prev = cur
	}
	dist := prev[m]
	cdf := make([]float64, len(dist))
	acc := 0.0
	for u, v := range dist {
		acc += v
		cdf[u] = acc
	}
	return cdf
}

// MannWhitneyP returns the two-sided p-value of the Mann-Whitney U test that
// xs and ys come from the same distribution, using the normal approximation
// with a tie correction and a continuity correction. It returns 1 when every
// value is tied.
func MannWhitneyP(xs, ys []float64) float64 {
	n, m := len(xs), len(ys)
	if n == 0 || m == 0 {
		return 1
	}
	type obs struct {
		v float64
		y bool
	}
	all := make([]obs, 0, n+m)
	for _, x := range xs {
		all = append(all, obs{x, false})
	}
	for _, y := range ys {
		all = append(all, obs{y, true})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v < all[j].v })
	N := float64(n + m)
	rankSumY := 0.0
	tieTerm := 0.0
	for i := 0; i < len(all); {
		j := i
		for j < len(all) && all[j].v == all[i].v {
			j++
		}
		r := float64(i+j+1) / 2 // average of ranks i+1..j
		t := float64(j - i)
		tieTerm += t*t*t - t
		for k := i; k < j; k++ {
			if all[k].y {
				rankSumY += r
			}
		}
		i = j
	}
	U := rankSumY - float64(m)*float64(m+1)/2
	mean := float64(n) * float64(m) / 2
	variance := float64(n) * float64(m) / 12 * ((N + 1) - tieTerm/(N*(N-1)))
	if variance <= 0 {
		return 1
	}
	z := (math.Abs(U-mean) - 0.5) / math.Sqrt(variance)
	if z < 0 {
		z = 0
	}
	return math.Erfc(z / math.Sqrt2)
}

// normalQuantile returns the p-quantile of the standard normal distribution.
func normalQuantile(p float64) float64 {
	return math.Sqrt2 * math.Erfinv(2*p-1)
}
