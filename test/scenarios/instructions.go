// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"strings"

	"github.com/just-very-quickly/jvq/internal/fixture"
)

// instructionRuns gates the fixture program on its instruction count,
// counted under valgrind: no change, a change inside the 1% budget, one
// over it, and that one again beside the noisy neighbour, which changes
// how long the program takes but not what it does; then a Go program with
// goroutines and garbage collection, unchanged.
func instructionRuns(bin string, hogCPUs, hogMB, n int) []trial {
	cfg := fixture.ProgConfig("instructions", 5, 200, "max_rounds = 30\n")
	cases := []struct {
		label string
		head  fixture.Spec
		hog   bool
	}{
		{"no change", fixture.Spec{Work: work, Extra: "nothing measured changed"}, false},
		{"+0.5% work (within the 1% budget)", fixture.Spec{Work: work * 1005 / 1000}, false},
		{"+2% work", fixture.Spec{Work: work * 102 / 100}, false},
		{"+2% work, beside the neighbour", fixture.Spec{Work: work * 102 / 100}, true},
		{"no change, beside the neighbour", fixture.Spec{Work: work, Extra: "nothing measured changed"}, true},
	}
	var trials []trial
	for _, c := range cases {
		r := mustRepo(fixture.Spec{Work: work}, c.head, cfg)
		var stop func()
		if c.hog {
			stop = startHog(hogCPUs, hogMB)
		}
		for i := 0; i < n; i++ {
			t := cli(bin, r.ModDir(), "instructions", c.label, "command")
			trials = append(trials, t)
			progress(t, i+1, n)
		}
		if stop != nil {
			stop()
		}
		os.RemoveAll(r.Dir)
	}
	// A busy Go program: 8 goroutines allocating, so the garbage collector
	// runs. Under valgrind, Go's spinning idle threads would make its count
	// jump between runs; jvq runs it with GOMAXPROCS=1.
	gcfg := strings.Replace(fixture.ProgConfig("instructions", 5, 400000, "max_rounds = 30\n"), "-reps 400000", "-reps 400000 -goroutines 8", 1)
	r := mustRepo(fixture.Spec{Work: 10}, fixture.Spec{Work: 10, Extra: "nothing measured changed"}, gcfg)
	const label = "no change, 8 goroutines allocating"
	for i := 0; i < n; i++ {
		t := cli(bin, r.ModDir(), "instructions", label, "command")
		trials = append(trials, t)
		progress(t, i+1, n)
	}
	os.RemoveAll(r.Dir)
	return trials
}
