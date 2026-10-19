// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const full = `
# jvq.toml
command = "go test -run=^$ -bench=. -count=1 ./..."
adapter = "go"
build   = ""          # runs once per side
rounds  = 12
max_rounds = 40
warmup  = false
confidence = 0.99
timeout = "90s"
fail_on_inconclusive = true

[budget]
time   = "5%"
allocs = "0"
bytes  = "10%"

[budget.bench."BenchmarkParse*"]
time = "2%"

[budget.bench."BenchmarkSlow/*"]
allocs = "off"

[ignore]
bench = [
  "BenchmarkFlaky*",   # sometimes slow
  'BenchmarkNet#1',
]
`

func TestParseFull(t *testing.T) {
	c, err := Parse(full)
	if err != nil {
		t.Fatal(err)
	}
	if c.Command != "go test -run=^$ -bench=. -count=1 ./..." || c.Adapter != "go" || c.Build != "" {
		t.Errorf("strings: %+v", c)
	}
	if c.Rounds != 12 || c.MaxRounds != 40 || c.Warmup || c.Confidence != 0.99 || c.Timeout != 90*time.Second || !c.FailOnInconclusive {
		t.Errorf("numbers: %+v", c)
	}
	if !c.Budget.Time.Pct || c.Budget.Time.Val != 0.05 {
		t.Errorf("time budget %+v", c.Budget.Time)
	}
	if c.Budget.Allocs.Pct || c.Budget.Allocs.Val != 0 || c.Budget.Allocs.Off {
		t.Errorf("allocs budget %+v", c.Budget.Allocs)
	}
	if !c.Budget.Bytes.Pct || c.Budget.Bytes.Val != 0.10 {
		t.Errorf("bytes budget %+v", c.Budget.Bytes)
	}
	if !reflect.DeepEqual(c.Ignore, []string{"BenchmarkFlaky*", "BenchmarkNet#1"}) {
		t.Errorf("ignore %q", c.Ignore)
	}
	if got := c.BudgetFor("time", "BenchmarkParse/small", "pkg.BenchmarkParse/small"); got.Val != 0.02 {
		t.Errorf("override: %+v", got)
	}
	if got := c.BudgetFor("time", "BenchmarkOther", "pkg.BenchmarkOther"); got.Val != 0.05 {
		t.Errorf("default: %+v", got)
	}
	if got := c.BudgetFor("allocs", "BenchmarkSlow/x", ""); !got.Off {
		t.Errorf("allocs off override: %+v", got)
	}
	if got := c.BudgetFor("allocs", "BenchmarkParse", ""); got.Off || got.Val != 0 {
		t.Errorf("allocs not overridden for time-only table: %+v", got)
	}
	if got := c.BudgetFor("MB/s", "BenchmarkParse", ""); !got.Off {
		t.Errorf("custom metrics are never gated: %+v", got)
	}
	if !c.Ignored("BenchmarkFlakyNet", "") || c.Ignored("BenchmarkParse", "") {
		t.Error("ignore matching")
	}
}

func TestDefaults(t *testing.T) {
	c, err := Parse("")
	if err != nil {
		t.Fatal(err)
	}
	d := Default()
	if !reflect.DeepEqual(c, d) {
		t.Errorf("empty file %+v differs from defaults %+v", c, d)
	}
	if d.Rounds != 10 || d.MaxRounds != 30 || !d.Warmup || d.Confidence != 0.95 || d.Budget.Time.Raw != "5%" || !d.Budget.Allocs.Off || !d.Budget.Bytes.Off {
		t.Errorf("defaults %+v", d)
	}
}

func TestErrorsNameTheProblem(t *testing.T) {
	cases := map[string]string{
		`comand = "x"`:                        `unknown setting "comand"`,
		`rounds = 2`:                          "rounds = 2",
		`max_rounds = 2000`:                   "max_rounds = 2000",
		`rounds = "ten"`:                      "whole number",
		`warmup = "yes"`:                      "true or false",
		`confidence = 1`:                      "confidence",
		`timeout = "soon"`:                    "timeout",
		`timeout = "-1s"`:                     "above zero",
		`adapter = "criterion"`:               `adapter "criterion"`,
		"[budget]\ntime = \"5ms\"":            "percentage",
		"[budget]\nspeed = \"5%\"":            `unknown budget "speed"`,
		"[budget]\nallocs = \"lots\"":         "allocs budget",
		"[budget]\ntime = true":               "string",
		"[ignore]\nbench = \"x\"":             "list",
		"[ignore]\nbench = [1]":               "strings",
		"[ignore]\nnames = []":                "ignore.names",
		"command = \"a\"\ncommand = \"b\"":    "set twice",
		"[budget]\n[budget]":                  "appears twice",
		"x = {a = 1}":                         "inline tables",
		"x.y = 1":                             "dotted keys",
		"[[x]]":                               "arrays of tables",
		"x = [1, [2]]":                        "nested arrays",
		"x = [1, 2":                           "never closed",
		"x = \"abc":                           "never closed",
		"x = abc":                             "need quotes",
		"[budget\nx=1":                        "closing ]",
		"just words":                          "key = value",
		"x = 1 2":                             "unexpected",
		`x = "\q"`:                            "escape",
		"budget = 5":                          "must be a table",
		"[budget.bench]\nx = 1":               "budget.bench",
		"x = '''a'''":                         "multi-line",
		"command = 1":                         "must be a string",
		"[budget]\ntime = \"-5%\"":            "percentage",
		"[budget.bench.\"B*\"]\ntime = \"x\"": "B*",
		"output = \"\"":                       "output is empty",
		"adapter = \"time\"\noutput = \"r.json\"": "time adapter reads no results",
	}
	for src, want := range cases {
		_, err := Parse(src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q): error %v, want it to mention %q", src, err, want)
		}
	}
}

func TestTOMLValues(t *testing.T) {
	m, err := parseTOML(`
a = "x \"q\" \\ \t#not a comment"
b = 'C:\path\#1'
c = 1_000
d = 2.5
e = -3
f = [ "a", 'b', 3, true ]
g = []
"quoted key" = 1
[t."a.b"]
h = false
`)
	if err != nil {
		t.Fatal(err)
	}
	if m["a"] != "x \"q\" \\ \t#not a comment" || m["b"] != `C:\path\#1` {
		t.Errorf("strings %q %q", m["a"], m["b"])
	}
	if m["c"] != int64(1000) || m["d"] != 2.5 || m["e"] != int64(-3) {
		t.Errorf("numbers %v %v %v", m["c"], m["d"], m["e"])
	}
	if !reflect.DeepEqual(m["f"], []any{"a", "b", int64(3), true}) {
		t.Errorf("array %v", m["f"])
	}
	if g, ok := m["g"].([]any); !ok || len(g) != 0 {
		t.Errorf("empty array %#v", m["g"])
	}
	if m["quoted key"] != int64(1) {
		t.Errorf("quoted key %v", m)
	}
	tt := m["t"].(map[string]any)["a.b"].(map[string]any)
	if tt["h"] != false {
		t.Errorf("table %v", tt)
	}
}

func TestParseBudget(t *testing.T) {
	cases := []struct {
		metric, in string
		want       Budget
	}{
		{"time", "5%", Budget{Pct: true, Val: 0.05, Raw: "5%"}},
		{"time", "0.5 %", Budget{Pct: true, Val: 0.005, Raw: "0.5 %"}},
		{"allocs", "0", Budget{Val: 0, Raw: "0"}},
		{"bytes", "64", Budget{Val: 64, Raw: "64"}},
		{"bytes", "off", Budget{Off: true, Raw: "off"}},
	}
	for _, c := range cases {
		got, err := ParseBudget(c.metric, c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseBudget(%q,%q) = %+v, %v", c.metric, c.in, got, err)
		}
	}
	for _, bad := range []string{"", "x%", "NaN", "-1", "1e999"} {
		if _, err := ParseBudget("bytes", bad); err == nil {
			t.Errorf("ParseBudget(bytes,%q) accepted", bad)
		}
	}
	if b, _ := ParseBudget("time", "off"); !b.Off || b.String() != "off" {
		t.Errorf("time off: %+v", b)
	}
	if s := (Budget{Pct: true, Val: 0.05, Raw: "5%"}).String(); s != "5%" {
		t.Errorf("String %q", s)
	}
}

func TestBudgetNumbersWithoutQuotes(t *testing.T) {
	c, err := Parse("[budget]\nallocs = 0\nbytes = 64.5")
	if err != nil {
		t.Fatal(err)
	}
	if c.Budget.Allocs.Val != 0 || c.Budget.Allocs.Off || c.Budget.Bytes.Val != 64.5 {
		t.Errorf("%+v", c.Budget)
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		p, n string
		want bool
	}{
		{"BenchmarkParse*", "BenchmarkParse", true},
		{"BenchmarkParse*", "BenchmarkParse/small/x", true},
		{"BenchmarkParse", "BenchmarkParseX", false},
		{"*Parse*", "BenchmarkParseJSON", true},
		{"Benchmark?ar", "BenchmarkBar", true},
		{"Benchmark?ar", "BenchmarkBaar", false},
		{"*", "", true},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"", "x", false},
	}
	for _, c := range cases {
		if got := Match(c.p, c.n); got != c.want {
			t.Errorf("Match(%q, %q) = %v", c.p, c.n, got)
		}
	}
}

func TestFindAndLoad(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Find(sub, root); got != "" {
		t.Errorf("found %q in an empty tree", got)
	}
	p := filepath.Join(root, FileName)
	if err := os.WriteFile(p, []byte("rounds = 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Find(sub, root); got != p {
		t.Errorf("Find = %q, want %q", got, p)
	}
	c, err := Load(p)
	if err != nil || c.Rounds != 5 || c.Source != p {
		t.Errorf("Load: %+v %v", c, err)
	}
	if _, err := Load(filepath.Join(root, "missing.toml")); err == nil || !strings.Contains(err.Error(), "doesn't exist") {
		t.Errorf("missing file: %v", err)
	}
	if err := os.WriteFile(p, []byte("rounds = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), p) {
		t.Errorf("bad file error should name it: %v", err)
	}
}

func TestLaterBenchTablesWin(t *testing.T) {
	// "Benchmark*" sorts before "*Parse*" alphabetically; file order must decide.
	c, err := Parse("[budget.bench.\"*Parse*\"]\ntime = \"2%\"\n[budget.bench.\"Benchmark*\"]\ntime = \"9%\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.BudgetFor("time", "BenchmarkParse", ""); got.Val != 0.09 {
		t.Errorf("the later table should win: %+v", got)
	}
	c, _ = Parse("[budget.bench.\"Benchmark*\"]\ntime = \"9%\"\n[budget.bench.\"*Parse*\"]\ntime = \"2%\"\n")
	if got := c.BudgetFor("time", "BenchmarkParse", ""); got.Val != 0.02 {
		t.Errorf("the later table should win: %+v", got)
	}
}

func TestFindThroughASymlink(t *testing.T) {
	real := t.TempDir()
	sub := filepath.Join(real, "a")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(real, FileName), []byte(""), 0o644)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	// cwd through the link, root resolved (as git reports it)
	if got := Find(filepath.Join(link, "a"), real); got == "" {
		t.Error("jvq.toml not found through a symlinked folder")
	}
	// a sibling whose name starts with the root's name isn't inside it
	sibling := real + "2"
	os.MkdirAll(sibling, 0o755)
	defer os.RemoveAll(sibling)
	if got := Find(sibling, real); got != "" {
		t.Errorf("found %q from a folder outside the root", got)
	}
}

func TestUnderscoresOnlyBetweenDigits(t *testing.T) {
	for _, bad := range []string{"x = 1__0", "x = _5", "x = 5_"} {
		if _, err := parseTOML(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if m, err := parseTOML("x = 1_000_000\ny = 1_0.5"); err != nil || m["x"] != int64(1000000) || m["y"] != 10.5 {
		t.Errorf("%v %v", m, err)
	}
}

func TestAdaptersAndOutput(t *testing.T) {
	for _, a := range []string{"go", "json", "time"} {
		c, err := Parse("adapter = \"" + a + "\"")
		if err != nil || c.Adapter != a {
			t.Errorf("adapter %s: %+v %v", a, c.Adapter, err)
		}
	}
	c, err := Parse("adapter = \"json\"\noutput = \"out/results.json\"")
	if err != nil || c.Output != "out/results.json" {
		t.Errorf("output: %q %v", c.Output, err)
	}
	c = Default()
	c.Adapter, c.Output = "time", "x"
	if c.Check() == nil {
		t.Error("Check let the time adapter read a file")
	}
	c.Adapter = "nope"
	if c.Check() == nil {
		t.Error("Check let an unknown adapter through")
	}
}
