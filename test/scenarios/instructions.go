// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"github.com/just-very-quickly/jvq/internal/fixture"
)

// instructionRuns gates the fixture program on its instruction count,
// counted under valgrind: no change, a change inside the 1% budget, one
// over it, and that one again beside the noisy neighbour, which changes
// how long the program takes but not what it does.
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
	return trials
}
