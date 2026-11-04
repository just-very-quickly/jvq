// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package flaky

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fullLine is an entry in the format jvq 0.7 and 0.8 wrote.
func fullLine(t *testing.T, e Entry) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sameEntries(t *testing.T, got, want []Entry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("entry %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

// Entries written compact read back exactly, and a file in the old format
// is converted the next time a run is added, losing nothing.
func TestCompactRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "h.jsonl")
	var old []string
	var want []Entry
	for i := 0; i < 5; i++ {
		e := Entry{Kind: Kind, JVQ: "0.8.0", Date: fmt.Sprintf("2026-11-0%dT00:00:00Z", i+1), Commit: fmt.Sprint("c", i),
			Tests: map[string]string{"a.TestA": "p", "a.TestB": "fp", "a (package)": "p", "b.TestC": "", "b.TestD/x y": "s", fmt.Sprint("a.TestNew", i): "f"}}
		want = append(want, e)
		old = append(old, fullLine(t, e))
	}
	os.WriteFile(path, []byte(strings.Join(old, "\n")+"\n"), 0o644)
	got, bad, _, err := Load(path)
	if err != nil || bad != 0 {
		t.Fatalf("old format: bad %d err %v", bad, err)
	}
	sameEntries(t, got, want)

	next := NewEntry("0.9.0", time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), "c5", map[string]string{"a.TestA": "pp", "a.TestB": "p"})
	if err := Append(path, next, 0); err != nil {
		t.Fatal(err)
	}
	want = append(want, next)
	got, bad, _, err = Load(path)
	if err != nil || bad != 0 {
		t.Fatalf("compact: bad %d err %v", bad, err)
	}
	sameEntries(t, got, want)
	b, _ := os.ReadFile(path)
	if bytes.Contains(b, []byte(`"tests":`)) {
		t.Errorf("old lines weren't converted:\n%s", b)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if !strings.Contains(lines[len(lines)-1], `"kind":"test-names"`) {
		t.Errorf("names line isn't last: %s", lines[len(lines)-1])
	}
	// Trimming keeps the newest entries and rewrites the names list for
	// them alone.
	if err := Append(path, next, 2); err != nil {
		t.Fatal(err)
	}
	got, _, _, _ = Load(path)
	sameEntries(t, got, []Entry{next, next})
	b, _ = os.ReadFile(path)
	if bytes.Contains(b, []byte("TestNew")) {
		t.Errorf("names of trimmed runs kept:\n%s", b)
	}
}

// Compact lines that don't add up are damaged lines: skipped and counted,
// never read as other tests' results.
func TestCompactDamaged(t *testing.T) {
	names := []string{"a.T1", "a.T2", "a.T3"}
	id := namesID(names)
	nl, _ := json.Marshal(lineHead{Kind: NamesKind, ID: id, List: names})
	entry := func(id string, results string) string {
		return `{"kind":"tests","jvq":"0.9.0","date":"2026-11-04T00:00:00Z","commit":"c","names_id":"` + id + `","results":` + results + `}`
	}
	cases := []struct {
		name, line string
		ok         bool
	}{
		{"good", entry(id, `{"p":"0-1","f":"2"}`), true},
		{"unknown names list", entry("0123456789abcdef", `{"p":"0-2"}`), false},
		{"number past the list", entry(id, `{"p":"0-3"}`), false},
		{"test twice", entry(id, `{"p":"0-1","f":"1"}`), false},
		{"numbers going down", entry(id, `{"p":"2,0"}`), false},
		{"range of one", entry(id, `{"p":"1-1"}`), false},
		{"leading zero", entry(id, `{"p":"01"}`), false},
		{"sign", entry(id, `{"p":"+1"}`), false},
		{"empty list", entry(id, `{"p":""}`), false},
		{"bad letters", entry(id, `{"pz":"0"}`), false},
		{"no results", entry(id, `null`), false},
		{"tests and results", strings.Replace(entry(id, `{"p":"0"}`), `"commit":"c"`, `"commit":"c","tests":{"x":"p"}`, 1), false},
		{"huge number", entry(id, `{"p":"0-99999999999"}`), false},
	}
	for _, c := range cases {
		src := c.line + "\n" + string(nl) + "\n"
		got, bad, err := Read(strings.NewReader(src))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.ok != (len(got) == 1 && bad == 0) {
			t.Errorf("%s: entries %d bad %d", c.name, len(got), bad)
		}
	}
	// A names list edited after it was written no longer matches its id,
	// so its entries can't be read with the wrong names.
	tampered := strings.Replace(string(nl), "a.T2", "a.T9", 1)
	got, bad, _ := Read(strings.NewReader(entry(id, `{"p":"0-2"}`) + "\n" + tampered + "\n"))
	if len(got) != 0 || bad != 1 {
		t.Errorf("edited names list: entries %d bad %d", len(got), bad)
	}
}

// oldAppend is what jvq 0.8 and earlier did on a write: keep every line
// it found, add a full line, keep the newest keep lines.
func oldAppend(t *testing.T, path string, e Entry, keep int) {
	t.Helper()
	b, _ := os.ReadFile(path)
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	lines = append(lines, fullLine(t, e))
	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// An older jvq can't read compact lines (it sees an empty history and
// skips them as damaged, so nothing it judges is in quarantine and every
// failure blocks), but it keeps them. Because the names list goes last,
// it is trimmed only after every entry that needs it, so a newer jvq
// still reads all of them.
func TestOlderJVQWritesIntoCompactFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.jsonl")
	ent := func(i int) Entry {
		return Entry{Kind: Kind, JVQ: "x", Date: "2026-11-04T00:00:00Z", Commit: fmt.Sprint("c", i), Tests: map[string]string{"a.T": "p", fmt.Sprint("a.U", i): "f"}}
	}
	for i := 0; i < 4; i++ {
		if err := Append(path, ent(i), 10); err != nil {
			t.Fatal(err)
		}
	}
	// What jvq 0.8's reader made of it.
	b, _ := os.ReadFile(path)
	oldBad := 0
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e Entry
		if json.Unmarshal([]byte(l), &e) != nil || !valid(e) {
			oldBad++
		}
	}
	if oldBad != 5 {
		t.Errorf("0.8 would read %d of 5 lines as damaged", oldBad)
	}
	for i := 4; i < 10; i++ {
		oldAppend(t, path, ent(i), 10)
		got, bad, err := Read(bytes.NewReader(must(os.ReadFile(path))))
		if err != nil || bad != 0 {
			t.Fatalf("after old write %d: bad %d err %v", i, bad, err)
		}
		if got[len(got)-1].Commit != fmt.Sprint("c", i) {
			t.Fatalf("after old write %d: newest %s", i, got[len(got)-1].Commit)
		}
	}
	// Back on the new jvq, the file is compact again.
	if err := Append(path, ent(10), 10); err != nil {
		t.Fatal(err)
	}
	got, bad, _ := Read(bytes.NewReader(must(os.ReadFile(path))))
	if len(got) != 10 || bad != 0 || got[0].Commit != "c1" {
		t.Errorf("entries %d bad %d first %s", len(got), bad, got[0].Commit)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// The compact format keeps a big suite's history small: 2000 Go tests with
// long names over 200 runs, a few failing each run. The sizes are logged
// for the reference.
func TestCompactHistorySize(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 27 MB file")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "h.jsonl")
	var ids []string
	for p := 0; p < 100; p++ {
		for k := 0; k < 20; k++ {
			ids = append(ids, fmt.Sprintf("github.com/acme/shop/internal/pkg%02d.TestSomething%04d/case_%d", p, p*20+k, k%5))
		}
	}
	var old bytes.Buffer
	var last Entry
	for r := 0; r < 200; r++ {
		m := make(map[string]string, len(ids))
		for i, id := range ids {
			switch {
			case (i*7+r*13)%997 == 0:
				m[id] = "fp"
			case (i*11+r*3)%1499 == 0:
				m[id] = "f"
			case i%250 == r%250:
				m[id] = "s"
			default:
				m[id] = "p"
			}
		}
		last = Entry{Kind: Kind, JVQ: "0.8.0", Date: time.Date(2026, 11, 1, 0, r, 0, 0, time.UTC).Format(time.RFC3339), Commit: fmt.Sprintf("%040x", r), Tests: m}
		if r < 199 {
			old.WriteString(fullLine(t, last))
			old.WriteByte('\n')
		}
	}
	os.WriteFile(path, old.Bytes(), 0o644)
	if err := Append(path, last, 200); err != nil {
		t.Fatal(err)
	}
	oldSize := old.Len() + len(fullLine(t, last)) + 1
	st, _ := os.Stat(path)
	if st.Size() > 1<<20 {
		t.Errorf("compact history is %d bytes", st.Size())
	}
	start := time.Now()
	entries, bad, _, err := Load(path)
	if err != nil || bad != 0 || len(entries) != 200 {
		t.Fatalf("entries %d bad %d err %v", len(entries), bad, err)
	}
	run := entries[len(entries)-1]
	Judge(entries[:199], &run, DefaultSettings())
	t.Logf("2000 tests, 200 runs: %.1f MB in the full format, %d KB compact; reading and judging took %v",
		float64(oldSize)/1e6, (st.Size()+500)/1000, time.Since(start).Round(10*time.Millisecond))
}
