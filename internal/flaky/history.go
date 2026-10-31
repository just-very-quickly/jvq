// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package flaky

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// line doesn't lose the rest.
func Read(r io.Reader) (entries []Entry, bad int, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 256*1024*1024)
	for sc.Scan() {
		l := bytes.TrimSpace(sc.Bytes())
		if len(l) == 0 {
			continue
		}
		var e Entry
		if json.Unmarshal(l, &e) != nil || !valid(e) {
			if isRunHistory(l) {
				return nil, 0, ErrRunHistory
			}
			bad++
			continue
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return entries, bad, err
	}
	return entries, bad, nil
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
// newest keep lines. Damaged lines stay where they are until they're
// trimmed, in case a newer jvq wrote them. The file is rewritten through a
// temporary file beside it, so a crash can't leave it half written; a
// symlink is followed, and the file keeps its mode.
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
			l = bytes.TrimSpace(l)
			if len(l) == 0 {
				continue
			}
			if isRunHistory(l) {
				return fmt.Errorf("%s: %w", path, ErrRunHistory)
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
