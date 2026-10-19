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
	"sort"
	"strings"
	"time"

	"github.com/just-very-quickly/jvq/internal/verdict"
)

// Entry is one run.
type Entry struct {
	JVQ     string   `json:"jvq"`
	Date    string   `json:"date"` // RFC 3339, UTC
	Base    Commit   `json:"base"`
	Head    Commit   `json:"head"`
	Verdict string   `json:"verdict"`
	Results []Result `json:"results"`
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
func FromRows(jvq string, when time.Time, base, head Commit, overall string, rows []verdict.Row) Entry {
	e := Entry{JVQ: jvq, Date: when.UTC().Format(time.RFC3339), Base: base, Head: head, Verdict: overall}
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
		}
		e.Results = append(e.Results, res)
	}
	return e
}

func ptr(f float64) *float64 { return &f }

// DefaultKeep is how many entries Append keeps by default.
const DefaultKeep = 1000

// Append adds e to the file at path, creating it if needed, and keeps only
// the newest keep entries. The file is rewritten through a temporary file
// in the same folder, so a crash can't leave it half written.
func Append(path string, e Entry, keep int) error {
	if keep < 1 {
		keep = DefaultKeep
	}
	var lines [][]byte
	if b, err := os.ReadFile(path); err == nil {
		for _, l := range bytes.Split(b, []byte("\n")) {
			if len(bytes.TrimSpace(l)) > 0 {
				lines = append(lines, l)
			}
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
	dir, name := splitPath(path)
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	w := bufio.NewWriter(tmp)
	for _, l := range lines {
		w.Write(l)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func splitPath(p string) (dir, name string) {
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return ".", p
	}
	return p[:i+1], p[i+1:]
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
	// Changes are the per-run changes, oldest first, for runs that had a
	// change for it; Heads are the head medians of those runs.
	Changes []float64
	Heads   []float64
	// Compounded is the product of (1 + change) over the runs, minus one:
	// how far the benchmark has moved in all, run on run.
	Compounded float64
	// Regressions counts the runs that called it a regression.
	Regressions int
	First, Last string // the first and last runs' head commits
}

// Trends works out each benchmark's trend over entries, oldest first.
func Trends(entries []Entry) []Trend {
	by := map[string]*Trend{}
	for _, e := range entries {
		for _, r := range e.Results {
			if r.Change == nil {
				continue
			}
			k := r.Key()
			t, ok := by[k]
			if !ok {
				t = &Trend{Key: k, Package: r.Package, Name: r.Name, Metric: r.Metric, Unit: r.Unit, Compounded: 0, First: e.Head.SHA}
				by[k] = t
			}
			t.Changes = append(t.Changes, *r.Change)
			if r.HeadMedian != nil {
				t.Heads = append(t.Heads, *r.HeadMedian)
			}
			if r.Verdict == verdict.Regression {
				t.Regressions++
			}
			t.Last = e.Head.SHA
		}
	}
	out := make([]Trend, 0, len(by))
	for _, t := range by {
		c := 1.0
		for _, x := range t.Changes {
			c *= 1 + x
		}
		t.Compounded = c - 1
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Compounded != out[j].Compounded {
			return out[i].Compounded > out[j].Compounded
		}
		return out[i].Key < out[j].Key
	})
	return out
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
