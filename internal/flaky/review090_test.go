// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package flaky

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReviewAmplification(t *testing.T) {
	const N, E = 50000, 400
	names := make([]string, N)
	for i := range names {
		names[i] = fmt.Sprintf("t%06d", i)
	}
	id := namesID(names)
	var b bytes.Buffer
	for i := 0; i < E; i++ {
		fmt.Fprintf(&b, `{"kind":"tests","jvq":"x","date":"2026-11-04T00:00:00Z","commit":"c%d","names_id":%q,"results":{"f":"0-%d"}}`+"\n", i, id, N-1)
	}
	b.WriteString(`{"kind":"test-names","id":"` + id + `","names":["` + strings.Join(names, `","`) + `"]}` + "\n")
	size := b.Len()
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	start := time.Now()
	es, bad, err := Read(&b)
	runtime.ReadMemStats(&m1)
	total := 0
	for _, e := range es {
		total += len(e.Tests)
	}
	mb := (m1.TotalAlloc - m0.TotalAlloc) >> 20
	t.Logf("file %d bytes -> %d entries, %d results, bad %d, err %v, %v, %d MB allocated", size, len(es), total, bad, err, time.Since(start), mb)
	if total > MaxResults || mb > 1000 {
		t.Errorf("a small history expanded to %d results, %d MB", total, mb)
	}
	if len(es) == 0 || es[len(es)-1].Commit != fmt.Sprint("c", E-1) {
		t.Error("the newest runs should be the ones kept")
	}
}

func TestReviewZeroTestsEntry(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h.jsonl")
	if err := Append(p, NewEntry("x", time.Now(), "c1", map[string]string{}), 10); err != nil {
		t.Fatal(err)
	}
	if err := Append(p, NewEntry("x", time.Now(), "c2", map[string]string{"a": "p"}), 10); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	es, bad, _, err := Load(p)
	t.Logf("file:\n%s", raw)
	if err != nil || bad != 0 || len(es) != 2 {
		t.Errorf("got %d entries, %d bad, err %v; want 2, 0", len(es), bad, err)
	}
}

func TestReviewEmptyAttempts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h.jsonl")
	if err := Append(p, NewEntry("x", time.Now(), "c1", map[string]string{"a": "", "b": "f"}), 10); err != nil {
		t.Fatal(err)
	}
	es, bad, _, err := Load(p)
	raw, _ := os.ReadFile(p)
	t.Logf("%s", raw)
	if err != nil || bad != 0 || len(es) != 1 || es[0].Tests["a"] != "" || es[0].Tests["b"] != "f" || len(es[0].Tests) != 2 {
		t.Errorf("got %+v bad %d err %v", es, bad, err)
	}
}

func TestReviewPinAlwaysHidesBuildFailure(t *testing.T) {
	s := DefaultSettings()
	s.Pin = func(id string) string {
		if strings.HasPrefix(id, "example.com/shop") {
			return PinAlways
		}
		return ""
	}
	hist := []Entry{NewEntry("x", time.Now(), "c1", map[string]string{"example.com/shop.TestA": "p", "example.com/shop (package)": "p"})}
	run := NewEntry("x", time.Now(), "c2", map[string]string{"example.com/shop (build)": "f"})
	res := Judge(hist, &run, s)
	t.Logf("verdict %s rows %+v", res.Verdict, res.Rows)
	if res.Verdict != VerdictFail {
		t.Errorf("a compile error passed the gate because a pin matched the package's build entry")
	}
	run2 := NewEntry("x", time.Now(), "c3", map[string]string{"example.com/shop (package)": "f"})
	res = Judge(hist, &run2, s)
	if res.Verdict != VerdictFail {
		t.Errorf("a package crash (TestMain) passed: %+v", res.Rows)
	}
}

func TestReviewTruncatedNamesLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h.jsonl")
	for i := 0; i < 50; i++ {
		a := "p"
		if i%2 == 0 {
			a = "fp"
		}
		if err := Append(p, NewEntry("x", time.Now(), fmt.Sprintf("c%d", i), map[string]string{"pkg.TestFlake": a, "pkg.TestOK": "p"}), 50); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(p)
	os.WriteFile(p, raw[:len(raw)-10], 0o644) // cache cut short by a few bytes
	es, bad, _, _ := Load(p)
	t.Logf("after losing the last 10 bytes: %d entries read, %d bad (of 50)", len(es), bad)
	Append(p, NewEntry("x", time.Now(), "new", map[string]string{"pkg.TestOK": "p"}), 50)
	es, bad, _, _ = Load(p)
	t.Logf("after one more run: %d entries, %d bad lines still taking keep slots", len(es), bad)
	if len(es) < 40 || bad > 1 {
		t.Errorf("ten lost bytes lost %d of 50 runs (%d bad lines)", 50-len(es)+1, bad)
	}
}

// Review of 0.9: a names line that doesn't match its id is a damaged
// line, kept like the others, and counted.
func TestReviewBadNamesLineKept(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h.jsonl")
	os.WriteFile(p, []byte(`{"kind":"test-names","id":"0000000000000000","names":["a"]}`+"\n"), 0o644)
	if _, bad, _, _ := Load(p); bad != 1 {
		t.Errorf("bad %d, want 1", bad)
	}
	Append(p, NewEntry("x", time.Now(), "c", map[string]string{"a": "p"}), 10)
	raw, _ := os.ReadFile(p)
	if !bytes.Contains(raw, []byte("0000000000000000")) {
		t.Errorf("damaged names line dropped:\n%s", raw)
	}
}
