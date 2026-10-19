// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package history keeps one line per jvq run in a file, and reads the file
// back to show how each benchmark has moved across runs.
//
// Each line is a JSON object, so the file can be appended to, trimmed, and
// read by other tools. jvq compares base and head inside each run, on one
// machine, so the per-run change is what's worth keeping: medians from
// different runs may come from different machines and can't be compared
// directly, but changes can be chained. Ten merges that each made a
// benchmark 1% slower, each within its budget, add up to about 10.5%, and
// the history shows it.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/just-very-quickly/jvq/internal/stats"
	"github.com/just-very-quickly/jvq/internal/verdict"
)

// Entry is one run.
type Entry struct {
	JVQ        string   `json:"jvq"`
	Date       string   `json:"date"` // RFC 3339, UTC
	Base       Commit   `json:"base"`
	Head       Commit   `json:"head"`
	Verdict    string   `json:"verdict"`
	Confidence float64  `json:"confidence,omitempty"` // of the intervals, such as 0.95
	Results    []Result `json:"results"`
}

// Commit is one side of a run.
type Commit struct {
	Ref string `json:"ref,omitempty"`
	SHA string `json:"sha"`
}

// Result is one benchmark and metric of a run.
type Result struct {
	Package    string   `json:"package,omitempty"`
	Name       string   `json:"name"`
	Metric     string   `json:"metric"`
	Unit       string   `json:"unit"`
	BaseMedian *float64 `json:"base_median,omitempty"`
	HeadMedian *float64 `json:"head_median,omitempty"`
	Change     *float64 `json:"change,omitempty"`
	Lo         *float64 `json:"ci_lo,omitempty"`
	Hi         *float64 `json:"ci_hi,omitempty"`
	Verdict    string   `json:"verdict"`
}

// Key identifies a benchmark and metric across runs.
func (r Result) Key() string {
	k := r.Name
	if r.Package != "" {
		k = r.Package + "." + r.Name
	}
	return k + " " + r.Metric
}

// FromRows makes an entry from a run's rows.
func FromRows(jvq string, when time.Time, base, head Commit, overall string, confidence float64, rows []verdict.Row) Entry {
	e := Entry{JVQ: jvq, Date: when.UTC().Format(time.RFC3339), Base: base, Head: head, Verdict: overall, Confidence: confidence}
	for _, r := range rows {
		res := Result{Package: r.Package, Name: r.Name, Metric: r.Metric, Unit: r.Unit, Verdict: r.Verdict}
		if r.Base != nil {
			res.BaseMedian = ptr(r.Base.Median)
		}
		if r.Head != nil {
			res.HeadMedian = ptr(r.Head.Median)
		}
		if r.Change != nil && !math.IsInf(*r.Change, 0) && !math.IsNaN(*r.Change) {
			res.Change = ptr(*r.Change)
			if r.Lo != nil && r.Hi != nil {
				res.Lo, res.Hi = ptr(*r.Lo), ptr(*r.Hi)
			}
		}
		e.Results = append(e.Results, res)
	}
	return e
}

func ptr(f float64) *float64 { return &f }

// DefaultKeep is how many entries Append keeps by default.
const DefaultKeep = 1000

// Append adds e to the file at path, creating it if needed, and keeps only
// the newest keep entries. An earlier entry for the same base and head (a
// re-run of the same job) is replaced rather than counted twice. The file
// is rewritten through a temporary file beside it, so a crash can't leave
// it half written; a symlink is followed, and the file keeps its mode.
func Append(path string, e Entry, keep int) error {
	if keep < 1 {
		keep = DefaultKeep
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o644)
	var lines [][]byte
	if b, err := os.ReadFile(path); err == nil {
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
		for _, l := range bytes.Split(b, []byte("\n")) {
			if len(bytes.TrimSpace(l)) == 0 {
				continue
			}
			var old Entry
			if json.Unmarshal(l, &old) == nil && old.Base.SHA == e.Base.SHA && old.Head.SHA == e.Head.SHA && e.Head.SHA != "" {
				continue
			}
			lines = append(lines, l)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	lines = append(lines, line)
	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("%s: the folder doesn't exist", path)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	w := bufio.NewWriter(tmp)
	for _, l := range lines {
		w.Write(l)
		w.WriteByte('\n')
	}
	err = w.Flush()
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Last returns the newest entry in the file at path, if there is one.
func Last(path string) (Entry, bool) {
	fh, err := os.Open(path)
	if err != nil {
		return Entry{}, false
	}
	defer fh.Close()
	entries, _, err := Read(fh)
	if err != nil || len(entries) == 0 {
		return Entry{}, false
	}
	return entries[len(entries)-1], true
}

// Read reads a history file. Lines that aren't valid entries are skipped
// and counted, so one damaged line doesn't lose the rest.
func Read(r io.Reader) (entries []Entry, bad int, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		l := bytes.TrimSpace(sc.Bytes())
		if len(l) == 0 {
			continue
		}
		var e Entry
		if json.Unmarshal(l, &e) != nil || e.Head.SHA == "" {
			bad++
			continue
		}
		entries = append(entries, e)
	}
	return entries, bad, sc.Err()
}

// Trend is one benchmark and metric across the runs that measured it.
type Trend struct {
	Key     string
	Package string
	Name    string
	Metric  string
	Unit    string
	// Changes are the per-run changes, oldest first, of the runs that had
	// one for it.
	Changes []float64
	// Compounded is the product of (1 + change) over those runs, minus one:
	// how far the benchmark has moved in all, run on run.
	Compounded float64
	// Lo and Hi bound Compounded: each run's interval gives the spread of
	// its estimate on each side, and the spreads add up along the chain
	// (as variances, in log space), so a long history of noisy runs has a
	// wide interval, and a single run keeps its own. HasInterval is false when
	// a time run had no interval (too few samples).
	Lo, Hi      float64
	HasInterval bool
	// Regressions counts the runs that called it a regression.
	Regressions int
	// Gated is true when some run gated it (pass, improved, regression or
	// inconclusive), as against only reporting it.
	Gated bool
}

// Trends works out each benchmark's trend over entries, oldest first.
func Trends(entries []Entry) []Trend {
	type acc struct {
		t      Trend
		logSum float64
		loVar  float64 // spread below the estimate, in log space
		hiVar  float64 // and above it
		noCI   bool
	}
	by := map[string]*acc{}
	var keys []string
	for _, e := range entries {
		level := e.Confidence
		if level <= 0 || level >= 1 {
			level = 0.95
		}
		z := stats.NormalQuantile(1 - (1-level)/2)
		for _, r := range e.Results {
			if r.Change == nil || *r.Change <= -1 {
				continue
			}
			k := r.Key()
			a, ok := by[k]
			if !ok {
				a = &acc{t: Trend{Key: k, Package: r.Package, Name: r.Name, Metric: r.Metric, Unit: r.Unit}}
				by[k] = a
				keys = append(keys, k)
			}
			a.t.Changes = append(a.t.Changes, *r.Change)
			a.logSum += math.Log1p(*r.Change)
			switch {
			case r.Lo != nil && r.Hi != nil && *r.Lo > -1 && *r.Hi > -1:
				// Rank intervals aren't symmetric, so each side keeps its
				// own spread; a single run then gives back its own interval.
				c := math.Log1p(*r.Change)
				lo := math.Max(0, c-math.Log1p(*r.Lo)) / z
				hi := math.Max(0, math.Log1p(*r.Hi)-c) / z
				a.loVar += lo * lo
				a.hiVar += hi * hi
			case r.Metric == "time" || r.Metric == "instructions":
				a.noCI = true
			}
			if r.Verdict == verdict.Regression {
				a.t.Regressions++
			}
			switch r.Verdict {
			case verdict.Pass, verdict.Improved, verdict.Regression, verdict.Inconclusive:
				a.t.Gated = true
			}
		}
	}
	out := make([]Trend, 0, len(by))
	for _, k := range keys {
		a := by[k]
		t := a.t
		t.Compounded = math.Expm1(a.logSum)
		z := stats.NormalQuantile(0.975)
		t.Lo = math.Expm1(a.logSum - z*math.Sqrt(a.loVar))
		t.Hi = math.Expm1(a.logSum + z*math.Sqrt(a.hiVar))
		t.HasInterval = !a.noCI
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Compounded != out[j].Compounded {
			return out[i].Compounded > out[j].Compounded
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Over reports whether t has moved more than limit in all, beyond doubt:
// the low end of its interval is over the limit. Results no run gated, and
// time results without an interval, never count.
func (t Trend) Over(limit float64) bool {
	if !t.Gated || !t.HasInterval {
		return false
	}
	return t.Lo > limit
}

// Gaps counts the places where a run's base isn't the previous run's
// head, so some commits between them were never measured.
func Gaps(entries []Entry) int {
	n := 0
	for i := 1; i < len(entries); i++ {
		if entries[i].Base.SHA != entries[i-1].Head.SHA {
			n++
		}
	}
	return n
}

// Spark draws changes as a row of block characters, one per run: higher
// blocks for runs that made it slower or bigger, a dot for no change.
func Spark(changes []float64) string {
	blocks := []rune("▁▂▃▄▅▆▇█")
	max := 0.0
	for _, c := range changes {
		if a := math.Abs(c); a > max {
			max = a
		}
	}
	var b strings.Builder
	for _, c := range changes {
		switch {
		case math.Abs(c) < 0.0005 || max == 0:
			b.WriteRune('·')
		case c < 0:
			b.WriteRune('▁')
		default:
			i := int(math.Round(c / max * float64(len(blocks)-1)))
			if i < 1 {
				i = 1
			}
			b.WriteRune(blocks[i])
		}
	}
	return b.String()
}

// Pct formats a fraction as a signed percentage.
func Pct(f float64) string {
	if math.Abs(f) < 0.0005 {
		return "0.0%"
	}
	return fmt.Sprintf("%+.1f%%", f*100)
}
