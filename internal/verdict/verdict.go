// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package verdict compares the base and head samples of every benchmark
// against its budgets and decides pass, regression or inconclusive.
//
// Time is judged on the ratio head/base. The Hodges-Lehmann estimate of the
// shift in log time gives the ratio, and its rank-based confidence interval
// says how sure that ratio is:
//
//   - regression: the whole interval is above 1+budget. Even the most
//     generous reading of the data says head is over budget.
//   - pass: the whole interval is below 1+budget.
//   - inconclusive: the interval straddles 1+budget. The run can't tell
//     either way at this noise level.
//
// Bytes and allocations per op hardly vary between runs, so they are judged
// on the medians, with no statistics: head is a regression when its median is
// over the base median plus the budget.
package verdict

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/just-very-quickly/jvq/internal/adapter"
	"github.com/just-very-quickly/jvq/internal/config"
	"github.com/just-very-quickly/jvq/internal/stats"
)

// Outcomes, per row and overall.
const (
	Pass         = "pass"
	Improved     = "improved" // a pass where head is clearly faster or smaller
	Regression   = "regression"
	Inconclusive = "inconclusive"
	New          = "new"     // only on head
	Removed      = "removed" // only on base
	Info         = "info"    // reported, not gated
)

// Straddles is the note on a time row whose interval holds the budget:
// more samples could settle it either way.
const Straddles = "the interval straddles the budget"

// NeedsMore reports whether more rounds could change the outcome: some gated
// time row is inconclusive only because its interval straddles the budget,
// or is still too small to have an interval.
func NeedsMore(rows []Row) bool {
	for _, r := range rows {
		if r.Verdict == Inconclusive && r.Metric == adapter.Time &&
			(r.Note == Straddles || strings.HasPrefix(r.Note, "too few samples")) {
			return true
		}
	}
	return false
}

// MinSamples is the fewest samples per side jvq judges time on.
const MinSamples = 3

// Side summarises one side's samples of one metric.
type Side struct {
	N      int     `json:"n"`
	Median float64 `json:"median"`
}

// Row is the outcome for one metric of one benchmark.
type Row struct {
	Package string `json:"package,omitempty"`
	Name    string `json:"name"`
	Metric  string `json:"metric"`
	Unit    string `json:"unit"`
	Base    *Side  `json:"base,omitempty"`
	Head    *Side  `json:"head,omitempty"`
	// Change is head relative to base: 0.07 means 7% more. For time it is
	// the Hodges-Lehmann ratio minus one; otherwise the ratio of medians.
	Change *float64 `json:"change,omitempty"`
	// Lo and Hi bound Change at the run's confidence level (time only).
	Lo      *float64 `json:"ci_lo,omitempty"`
	Hi      *float64 `json:"ci_hi,omitempty"`
	P       *float64 `json:"p,omitempty"`
	Budget  string   `json:"budget"`
	Verdict string   `json:"verdict"`
	Note    string   `json:"note,omitempty"`
}

// Key is the row's benchmark key, as in adapter.Sample.Key.
func (r Row) Key() string {
	if r.Package == "" {
		return r.Name
	}
	return r.Package + "." + r.Name
}

// Gated reports whether the row's verdict counts towards the overall one.
func (r Row) Gated() bool {
	switch r.Verdict {
	case Pass, Improved, Regression, Inconclusive:
		return true
	}
	return false
}

type series struct {
	pkg, name, metric, unit string
	base, head              []float64
}

// Compare builds one row per benchmark and metric found on either side,
// sorted by benchmark key and then metric (time, bytes, allocs, others).
func Compare(base, head []adapter.Sample, cfg config.Config) []Row {
	all := map[string]*series{}
	add := func(s adapter.Sample, isHead bool) {
		k := s.Key() + "\x00" + s.Metric
		se, ok := all[k]
		if !ok {
			se = &series{pkg: s.Package, name: s.Name, metric: s.Metric, unit: s.Unit}
			all[k] = se
		}
		if isHead {
			se.head = append(se.head, s.Value)
		} else {
			se.base = append(se.base, s.Value)
		}
	}
	for _, s := range base {
		add(s, false)
	}
	for _, s := range head {
		add(s, true)
	}
	var rows []Row
	for _, se := range all {
		key := se.name
		if se.pkg != "" {
			key = se.pkg + "." + se.name
		}
		if cfg.Ignored(se.name, key) {
			continue
		}
		rows = append(rows, judge(se, cfg.BudgetFor(se.metric, se.name, key), cfg.Confidence))
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Key() != b.Key() {
			return a.Key() < b.Key()
		}
		return metricOrder(a.Metric) < metricOrder(b.Metric) ||
			(metricOrder(a.Metric) == metricOrder(b.Metric) && a.Metric < b.Metric)
	})
	return rows
}

func metricOrder(m string) int {
	switch m {
	case adapter.Time:
		return 0
	case adapter.Bytes:
		return 1
	case adapter.Allocs:
		return 2
	}
	return 3
}

func side(xs []float64) *Side {
	if len(xs) == 0 {
		return nil
	}
	return &Side{N: len(xs), Median: stats.Median(xs)}
}

func ptr(f float64) *float64 { return &f }

func judge(se *series, b config.Budget, level float64) Row {
	r := Row{Package: se.pkg, Name: se.name, Metric: se.metric, Unit: se.unit,
		Base: side(se.base), Head: side(se.head), Budget: b.String()}
	switch {
	case len(se.base) == 0:
		r.Verdict = New
		r.Note = "only on head"
		return r
	case len(se.head) == 0:
		r.Verdict = Removed
		r.Note = "only on base"
		return r
	}
	switch se.metric {
	case adapter.Time:
		judgeTime(&r, se, b, level)
	case adapter.Bytes, adapter.Allocs:
		judgeExact(&r, se, b)
	default:
		r.Verdict = Info
		if r.Base.Median != 0 {
			r.Change = ptr(r.Head.Median/r.Base.Median - 1)
		}
		r.Note = "not gated"
	}
	return r
}

func judgeTime(r *Row, se *series, b config.Budget, level float64) {
	// With the budget off the row is reported, never gated, whatever the
	// sample sizes; the estimate is still filled in when there is one.
	defer func() {
		if b.Off {
			r.Verdict = Info
			r.Note = "time budget off"
		}
	}()
	if len(se.base) < MinSamples || len(se.head) < MinSamples {
		r.Verdict = Inconclusive
		r.Note = fmt.Sprintf("too few samples (need %d a side)", MinSamples)
		return
	}
	lb, okb := logs(se.base)
	lh, okh := logs(se.head)
	if !okb || !okh {
		r.Verdict = Inconclusive
		r.Note = "a time of zero or less can't be compared as a ratio"
		return
	}
	sh, err := stats.HodgesLehmann(lb, lh, level)
	if err != nil {
		r.Verdict = Inconclusive
		r.Note = err.Error()
		return
	}
	r.Change = ptr(math.Exp(sh.Estimate) - 1)
	r.P = ptr(sh.P)
	if !sh.Bounded {
		r.Verdict = Inconclusive
		r.Note = "too few samples for an interval at this confidence level"
		return
	}
	lo, hi := math.Exp(sh.Lo)-1, math.Exp(sh.Hi)-1
	r.Lo, r.Hi = ptr(lo), ptr(hi)
	switch {
	case lo > b.Val:
		r.Verdict = Regression
	case hi <= b.Val:
		r.Verdict = Pass
		if hi < 0 && -hi > b.Val {
			r.Verdict = Improved
		}
	default:
		r.Verdict = Inconclusive
		r.Note = Straddles
	}
}

func logs(xs []float64) ([]float64, bool) {
	out := make([]float64, len(xs))
	for i, x := range xs {
		if !(x > 0) || math.IsInf(x, 0) {
			return nil, false
		}
		out[i] = math.Log(x)
	}
	return out, true
}

func judgeExact(r *Row, se *series, b config.Budget) {
	mb, mh := r.Base.Median, r.Head.Median
	if mb != 0 {
		r.Change = ptr(mh/mb - 1)
	} else if mh != 0 {
		r.Change = ptr(math.Inf(1))
	} else {
		r.Change = ptr(0)
	}
	if b.Off {
		r.Verdict = Info
		r.Note = r.Metric + " budget off"
		return
	}
	limit := mb + b.Val
	if b.Pct {
		limit = mb * (1 + b.Val)
	}
	switch {
	case mh > limit:
		r.Verdict = Regression
		r.Note = fmt.Sprintf("%s over the limit of %s", formatDelta(mh-mb), trim(limit))
	case mh < mb:
		r.Verdict = Improved
	default:
		r.Verdict = Pass
	}
}

func formatDelta(d float64) string {
	if d > 0 {
		return "+" + trim(d)
	}
	return trim(d)
}

func trim(f float64) string {
	return fmt.Sprintf("%.6g", f)
}

// Overall is the run's verdict: regression if any gated row is a
// regression, else inconclusive if any is inconclusive, else pass. A run
// with no gated rows is inconclusive.
func Overall(rows []Row) string {
	reg, inc, gated := false, false, false
	for _, r := range rows {
		if !r.Gated() {
			continue
		}
		gated = true
		switch r.Verdict {
		case Regression:
			reg = true
		case Inconclusive:
			inc = true
		}
	}
	switch {
	case reg:
		return Regression
	case inc || !gated:
		return Inconclusive
	}
	return Pass
}

// UngatedNote explains a run where no row is gated, which is otherwise
// puzzling: every budget off, or units jvq doesn't gate. It is "" when some
// row is gated.
func UngatedNote(rows []Row) string {
	var units []string
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Gated() {
			return ""
		}
		switch r.Metric {
		case adapter.Time, adapter.Bytes, adapter.Allocs:
		default:
			if !seen[r.Unit] {
				seen[r.Unit] = true
				units = append(units, fmt.Sprintf("%q", r.Unit))
			}
		}
	}
	if len(rows) == 0 {
		return ""
	}
	if len(units) > 0 {
		return "nothing was gated: jvq gates time (ns, us, ms, s), bytes (B) and allocs, and reports other units, here " + strings.Join(units, ", ") + ", without judging them"
	}
	return "nothing was gated: every result's budget is off, or it is only on one side"
}

// Counts tallies rows by verdict.
func Counts(rows []Row) map[string]int {
	c := map[string]int{}
	for _, r := range rows {
		c[r.Verdict]++
	}
	return c
}
