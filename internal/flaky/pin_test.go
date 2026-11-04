// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package flaky

import (
	"fmt"
	"testing"
)

func pinned(always, never string) Settings {
	s := DefaultSettings()
	s.Pin = func(id string) string {
		switch id {
		case always:
			return PinAlways
		case never:
			return PinNever
		}
		return ""
	}
	return s
}

// A test pinned always is in quarantine from its first run, so its
// failures don't block without any evidence, and passes don't take it
// out. It still breaks: failing every attempt three times in a row blocks,
// and it goes back into quarantine once an attempt passes.
func TestPinAlways(t *testing.T) {
	s := pinned("T", "")
	var h []Entry
	judge := func(c, a string) Result {
		run := Entry{Kind: Kind, Commit: c, Tests: map[string]string{"T": a}}
		r := Judge(h, &run, s)
		h = append(h, run)
		return r
	}
	r := judge("c0", "f")
	wantStatus(t, r, "T", StatusQuarantined, false)
	if x, _ := row(r, "T"); x.Pinned != PinAlways || x.Event != "" {
		t.Errorf("first run: %+v", x)
	}
	for i := 1; i <= 40; i++ {
		judge(fmt.Sprint("c", i), "p")
	}
	r = judge("c41", "f")
	wantStatus(t, r, "T", StatusQuarantined, false) // 40 passes didn't take it out
	judge("c42", "f")
	r = judge("c43", "f")
	wantStatus(t, r, "T", StatusBroken, true)
	r = judge("c44", "f")
	wantStatus(t, r, "T", StatusFailed, true)
	r = judge("c45", "p")
	if x, _ := row(r, "T"); !x.Quarantined || x.Event != EventEntered {
		t.Errorf("after a pass it should be back in quarantine: %+v", x)
	}
	r = judge("c46", "f")
	wantStatus(t, r, "T", StatusQuarantined, false)
	// Retries that end in a pass don't break it either.
	if r = judge("c47", "ffp"); r.Verdict != VerdictPass {
		t.Errorf("verdict %s", r.Verdict)
	}
}

// A test pinned never is never quarantined, however flaky: its failures
// always block. Its flaky commits are still counted and shown.
func TestPinNever(t *testing.T) {
	s := pinned("", "T")
	var h []Entry
	for i := 0; i < 5; i++ {
		h = append(h, Entry{Kind: Kind, Commit: fmt.Sprint("c", i), Tests: map[string]string{"T": "fp", "U": "fp"}})
	}
	r := Judge(h, &Entry{Commit: "c9", Tests: map[string]string{"T": "f", "U": "f"}}, s)
	wantStatus(t, r, "T", StatusFailed, true)
	wantStatus(t, r, "U", StatusQuarantined, false)
	if x, _ := row(r, "T"); x.Pinned != PinNever || x.FlakyCommits != 5 || x.Quarantined {
		t.Errorf("%+v", x)
	}
	if r.InQuarantine != 1 {
		t.Errorf("in quarantine %d", r.InQuarantine)
	}
}
