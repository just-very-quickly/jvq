// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"regexp"
	"testing"
)

// The README's examples and the GitLab template name a release. They must
// name this one, or a release would ship pointing users at the last.
func TestExamplesNameThisVersion(t *testing.T) {
	want := "v" + Version
	checks := []struct {
		file string
		re   string
	}{
		{"../../README.md", `just-very-quickly/jvq@(v[0-9]+\.[0-9]+\.[0-9]+)`},
		{"../../README.md", `jvq/(v[0-9.]+)/ci/gitlab/jvq\.yml`},
		{"../../ci/gitlab/jvq.yml", `JVQ_VERSION: (v[0-9]+\.[0-9]+\.[0-9]+)`},
		{"../../ci/gitlab/jvq.yml", `jvq/(v[0-9.]+)/ci/gitlab/jvq\.yml`},
		{"../../action.yml", `such as (v[0-9]+\.[0-9]+\.[0-9]+)`},
	}
	for _, c := range checks {
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		ms := regexp.MustCompile(c.re).FindAllStringSubmatch(string(b), -1)
		if len(ms) == 0 {
			t.Errorf("%s: nothing matches %s", c.file, c.re)
		}
		for _, m := range ms {
			if m[1] != want {
				t.Errorf("%s names %s, want %s", c.file, m[1], want)
			}
		}
	}
}
