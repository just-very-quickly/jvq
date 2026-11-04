// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package flaky

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kind marks a test history line, so a run history (jvq run -history)
// can't be mistaken for one.
const Kind = "tests"

// Entry is one CI run's test results: each test's attempts as letters,
// p pass, f fail, s skip, in order.
type Entry struct {
	Kind   string            `json:"kind"`
	JVQ    string            `json:"jvq"`
	Date   string            `json:"date"` // RFC 3339, UTC
	Commit string            `json:"commit,omitempty"`
	Tests  map[string]string `json:"tests"`
}

// NewEntry makes an entry for a run.
func NewEntry(jvq string, when time.Time, commit string, attempts map[string]string) Entry {
	return Entry{Kind: Kind, JVQ: jvq, Date: when.UTC().Format(time.RFC3339), Commit: commit, Tests: attempts}
}

// ErrRunHistory is returned for a file that holds jvq run's benchmark
// history, which jvq flaky must not write to.
var ErrRunHistory = errors.New("this is a benchmark history written by jvq run -history, not a test history; give jvq flaky a file of its own")

func valid(e Entry) bool {
	if e.Kind != Kind || e.Tests == nil {
		return false
	}
	for _, a := range e.Tests {
		for i := 0; i < len(a); i++ {
			switch a[i] {
			case 'p', 'f', 's':
			default:
				return false
			}
		}
	}
	return true
}

// isRunHistory reports whether a line is jvq run's history entry.
func isRunHistory(l []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(l, &m) != nil {
		return false
	}
	_, head := m["head"]
	_, results := m["results"]
	_, kind := m["kind"]
	return head && results && !kind
}

// Read reads a test history. Lines that aren't valid entries (damaged,
// cut short, or from something else) are skipped and counted, so one bad
// line doesn't lose the rest. Both formats are read: lines that spell out
// every test's name (jvq 0.7 and 0.8) and compact lines that refer to a
// names line (0.9 on).
func Read(r io.Reader) (entries []Entry, bad int, err error) {
	ls, err := readLines(r)
	if err != nil {
		return nil, 0, err
	}
	for _, l := range ls {
		if isRunHistory(l.raw) {
			return nil, 0, ErrRunHistory
		}
	}
	dicts := dictionaries(ls)
	// Newest first, so that past MaxResults it's the oldest runs that go.
	budget := MaxResults
	var rev []Entry
	for i := len(ls) - 1; i >= 0; i-- {
		l := ls[i]
		if l.names {
			continue
		}
		e, ok := decode(l, dicts, budget)
		if !ok {
			bad++
			continue
		}
		budget -= len(e.Tests)
		rev = append(rev, e)
	}
	for i := len(rev) - 1; i >= 0; i-- {
		entries = append(entries, rev[i])
	}
	return entries, bad, nil
}

// MaxResults bounds the test results a history is read into, all runs
// together: a compact history is small on disk and much bigger in memory,
// and it comes from a cache a pull request may have written. Past it, the
// oldest runs are skipped as damaged. 2000 tests over 200 runs are 400,000.
const MaxResults = 5_000_000

// line is one line of a history file, with what's needed to decode it.
type line struct {
	raw   []byte
	names bool // a names line
	head  lineHead
}

// lineHead holds every field a history line may have.
type lineHead struct {
	Kind    string            `json:"kind"`
	JVQ     string            `json:"jvq"`
	Date    string            `json:"date"`
	Commit  string            `json:"commit,omitempty"`
	Tests   map[string]string `json:"tests,omitempty"`
	Names   string            `json:"names_id,omitempty"` // a compact entry's names line
	Results map[string]string `json:"results,omitempty"`  // a compact entry's attempts -> name numbers
	ID      string            `json:"id,omitempty"`       // a names line's
	List    []string          `json:"names,omitempty"`    // a names line's
}

func readLines(r io.Reader) ([]line, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 256*1024*1024)
	var ls []line
	for sc.Scan() {
		l := bytes.TrimSpace(sc.Bytes())
		if len(l) == 0 {
			continue
		}
		raw := append([]byte(nil), l...)
		var h lineHead
		if json.Unmarshal(raw, &h) != nil {
			h = lineHead{}
		}
		ls = append(ls, line{raw: raw, names: h.Kind == NamesKind, head: h})
	}
	return ls, sc.Err()
}

// dictionaries returns the file's valid names lines by id. One that
// doesn't match its id is marked an ordinary line, so it's counted and
// kept as damaged.
func dictionaries(ls []line) map[string][]string {
	d := map[string][]string{}
	for i := range ls {
		l := &ls[i]
		if !l.names {
			continue
		}
		if l.head.ID != "" && l.head.ID == namesID(l.head.List) {
			d[l.head.ID] = l.head.List
		} else {
			l.names = false
		}
	}
	return d
}

// decode reads one entry; a compact one may hold at most budget results.
func decode(l line, dicts map[string][]string, budget int) (Entry, bool) {
	h := l.head
	e := Entry{Kind: h.Kind, JVQ: h.JVQ, Date: h.Date, Commit: h.Commit, Tests: h.Tests}
	if h.Names != "" || h.Results != nil {
		if h.Tests != nil {
			return Entry{}, false
		}
		names, ok := dicts[h.Names]
		if !ok || h.Results == nil {
			return Entry{}, false
		}
		n := 0
		for _, nums := range h.Results {
			c, ok := count(nums, len(names))
			if !ok {
				return Entry{}, false
			}
			n += c
		}
		if n > len(names) || n > budget {
			return Entry{}, false
		}
		e.Tests = map[string]string{}
		for attempts, nums := range h.Results {
			ok := true
			eachNumber(nums, len(names), func(i int) {
				if _, dup := e.Tests[names[i]]; dup {
					ok = false
				}
				e.Tests[names[i]] = attempts
			}, &ok)
			if !ok {
				return Entry{}, false
			}
		}
	}
	return e, valid(e)
}

// NamesKind marks a names line: the test names compact entries refer to
// by number.
const NamesKind = "test-names"

// namesID names a list of test names by its hash, so an entry can only be
// read with the very list it was written with.
func namesID(names []string) string {
	h := sha256.New()
	for _, n := range names {
		io.WriteString(h, n)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// eachNumber calls f for each number in a list such as "0-41,43,45-60",
// in order. Numbers must rise, and stay below n; ok is cleared otherwise.
func eachNumber(s string, n int, f func(int), ok *bool) {
	if s == "" {
		*ok = false
		return
	}
	next := 0
	for _, part := range strings.Split(s, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err1 := number(lo)
		b := a
		var err2 error
		if isRange {
			b, err2 = number(hi)
		}
		if err1 != nil || err2 != nil || a < next || b < a || (isRange && b == a) || b >= n {
			*ok = false
			return
		}
		for i := a; i <= b; i++ {
			f(i)
		}
		next = b + 1
	}
}

func number(s string) (int, error) {
	if s == "" || len(s) > 9 || (len(s) > 1 && s[0] == '0') {
		return 0, errors.New("bad number")
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("bad number")
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, nil
}

// count says how many numbers a list such as "0-41,43" holds, without
// expanding it, and whether it's well formed.
func count(s string, n int) (int, bool) {
	ok := true
	c := 0
	next := 0
	if s == "" {
		return 0, false
	}
	for _, part := range strings.Split(s, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err1 := number(lo)
		b := a
		var err2 error
		if isRange {
			b, err2 = number(hi)
		}
		if err1 != nil || err2 != nil || a < next || b < a || (isRange && b == a) || b >= n {
			return 0, false
		}
		c += b - a + 1
		next = b + 1
	}
	return c, ok
}

// numbers writes sorted numbers as a list such as "0-41,43".
func numbers(ns []int) string {
	var b strings.Builder
	for i := 0; i < len(ns); {
		j := i
		for j+1 < len(ns) && ns[j+1] == ns[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(ns[i]))
		if j > i {
			b.WriteByte('-')
			b.WriteString(strconv.Itoa(ns[j]))
		}
		i = j + 1
	}
	return b.String()
}

// compactLine is a compact entry as written: results always, even empty.
type compactLine struct {
	Kind    string            `json:"kind"`
	JVQ     string            `json:"jvq"`
	Date    string            `json:"date"`
	Commit  string            `json:"commit,omitempty"`
	Names   string            `json:"names_id"`
	Results map[string]string `json:"results"`
}

// compact writes entries in the compact format: each entry gives its
// tests as numbers in one names list, written before and after them. The
// copy at the end means an older jvq, which keeps lines it can't read and
// trims the oldest, drops it only after every entry that needs it; the
// one at the start survives a file cut short.
func compact(entries []Entry) (lines [][]byte, namesLine []byte, err error) {
	seen := map[string]bool{}
	var names []string
	for _, e := range entries {
		for id := range e.Tests {
			if !seen[id] {
				seen[id] = true
				names = append(names, id)
			}
		}
	}
	sort.Strings(names)
	num := make(map[string]int, len(names))
	for i, n := range names {
		num[n] = i
	}
	id := namesID(names)
	for _, e := range entries {
		by := map[string][]int{}
		for t, a := range e.Tests {
			by[a] = append(by[a], num[t])
		}
		results := make(map[string]string, len(by))
		for a, ns := range by {
			sort.Ints(ns)
			results[a] = numbers(ns)
		}
		l, err := json.Marshal(compactLine{Kind: Kind, JVQ: e.JVQ, Date: e.Date, Commit: e.Commit, Names: id, Results: results})
		if err != nil {
			return nil, nil, err
		}
		lines = append(lines, l)
	}
	namesLine, err = json.Marshal(struct {
		Kind  string   `json:"kind"`
		ID    string   `json:"id"`
		Names []string `json:"names"`
	}{NamesKind, id, names})
	return lines, namesLine, err
}

// Load reads the history file at path. A missing file is an empty history,
// with missing set.
func Load(path string) (entries []Entry, bad int, missing bool, err error) {
	fh, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, true, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	defer fh.Close()
	if st, err := fh.Stat(); err == nil && st.IsDir() {
		return nil, 0, false, fmt.Errorf("%s is a folder; give a file name", path)
	}
	entries, bad, err = Read(fh)
	if err != nil {
		return nil, 0, false, fmt.Errorf("%s: %w", path, err)
	}
	return entries, bad, false, nil
}

// Append adds e to the file at path, creating it if needed, and keeps the
// newest keep entries. It writes the compact format, converting older
// lines. Damaged lines stay where they are until they're trimmed, in case
// a newer jvq wrote them. The file is rewritten through a temporary file
// beside it, so a crash can't leave it half written; a symlink is
// followed, and the file keeps its mode.
func Append(path string, e Entry, keep int) error {
	if keep < 1 {
		keep = DefaultKeep
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o644)
	var ls []line
	if fh, err := os.Open(path); err == nil {
		if fi, err := fh.Stat(); err == nil {
			mode = fi.Mode().Perm()
		}
		ls, err = readLines(fh)
		fh.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for _, l := range ls {
			if isRunHistory(l.raw) {
				return fmt.Errorf("%s: %w", path, ErrRunHistory)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// The file's lines in order, names lines left out: they're written
	// again from the entries kept.
	type item struct {
		e   *Entry
		raw []byte
	}
	dicts := dictionaries(ls)
	var items []item
	budget := MaxResults
	for i := len(ls) - 1; i >= 0; i-- {
		l := ls[i]
		if l.names {
			continue
		}
		if d, ok := decode(l, dicts, budget); ok {
			budget -= len(d.Tests)
			items = append(items, item{e: &d})
		} else if l.head.Kind == Kind && l.head.Names != "" && dicts[l.head.Names] == nil {
			// A compact line whose names are gone can never be read
			// again: dropped rather than kept to fill the history.
		} else {
			items = append(items, item{raw: l.raw})
		}
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	items = append(items, item{e: &e})
	if len(items) > keep {
		items = items[len(items)-keep:]
	}
	var entries []Entry
	for _, it := range items {
		if it.e != nil {
			entries = append(entries, *it.e)
		}
	}
	encoded, namesLine, err := compact(entries)
	if err != nil {
		return err
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
	w.Write(namesLine)
	w.WriteByte('\n')
	k := 0
	for _, it := range items {
		if it.e != nil {
			w.Write(encoded[k])
			k++
		} else {
			w.Write(it.raw)
		}
		w.WriteByte('\n')
	}
	w.Write(namesLine)
	w.WriteByte('\n')
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
