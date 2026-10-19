// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"math"
	"os"
	"strings"
	"testing"
)

// The testdata files are real output, captured from criterion 0.5 (cargo
// bench, twice: the second run adds change lines, a wrapped long name and a
// throughput line), Google Benchmark 1.9.5 through its Python bindings,
// pytest-benchmark 5 (trimmed to five raw times per test) and vitest 3.2.

func parseFile(t *testing.T, adapter, file string) []Sample {
	t.Helper()
	f, err := os.Open("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := Parse(adapter, f)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return got
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b)) }

type want struct {
	pkg, name string
	ns        float64
}

func check(t *testing.T, got []Sample, ws []want) {
	t.Helper()
	if len(got) != len(ws) {
		t.Fatalf("got %d samples, want %d: %+v", len(got), len(ws), got)
	}
	for i, w := range ws {
		g := got[i]
		if g.Package != w.pkg || g.Name != w.name || g.Metric != Time || g.Unit != "ns" || !near(g.Value, w.ns) {
			t.Errorf("sample %d: %+v, want %+v", i, g, w)
		}
	}
}

func TestCriterion(t *testing.T) {
	check(t, parseFile(t, "criterion", "criterion-first.txt"), []want{
		{"", "xorshift 1000", 1873.2}, {"", "sizes/10", 8.6654}, {"", "sizes/10000", 18952},
	})
	check(t, parseFile(t, "criterion", "criterion.txt"), []want{
		{"", "xorshift 1000", 1844.0},
		{"", "a rather long benchmark name that wraps", 193.63},
		{"", "throughput/hash 4k", 823.22},
		{"", "sizes/10", 7.3142},
		{"", "sizes/10000", 19196},
	})
}

func TestCriterionBencherAndErrors(t *testing.T) {
	got, err := Parse("criterion", strings.NewReader("running 2 tests\ntest parse_small ... bench:       1,873 ns/iter (+/- 21)\ntest big   ... bench:  12.5 ms/iter (+/- 1)\ntest result: ok.\n"))
	if err != nil {
		t.Fatal(err)
	}
	check(t, got, []want{{"", "parse_small", 1873}, {"", "big", 12.5e6}})
	for in, msg := range map[string]string{
		"x  time:   [1 ns 2 ns]":                    "three values",
		"x  time:   [1 ns 2 parsecs 3 ns]":          "can't read",
		"         time:   [1.0 ns 1.1 ns 1.2 ns]\n": "no benchmark name",
	} {
		if _, err := Parse("criterion", strings.NewReader(in)); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: got %v, want %q", in, err, msg)
		}
	}
}

func TestGoogleBenchmark(t *testing.T) {
	check(t, parseFile(t, "google-benchmark", "google-benchmark.json"), []want{
		{"", "parse_small", 15843.314830274634}, {"", "sum_range/10", 210.27273939529488}, {"", "sum_range/1000", 14860.335384066775},
	})
	// With repetitions, every repetition is a sample and the aggregates
	// (mean, median, stddev, cv) are left out.
	got := parseFile(t, "google-benchmark", "google-benchmark-repetitions.json")
	if len(got) != 9 || got[0].Name != "parse_small" || got[3].Name != "sum_range/10" {
		t.Errorf("repetitions: %+v", got)
	}
	ms, err := Parse("google-benchmark", strings.NewReader(`{"benchmarks": [{"name": "a", "run_type": "iteration", "real_time": 2.5, "time_unit": "ms"}]}`))
	if err != nil || len(ms) != 1 || ms[0].Value != 2.5e6 {
		t.Errorf("ms: %+v %v", ms, err)
	}
	for in, msg := range map[string]string{
		`{"context": {}}`: "--benchmark_format=json",
		`{"benchmarks": [{"name": "a", "error_occurred": true, "error_message": "boom"}]}`: "boom",
		"a console table\n": "google-benchmark",
	} {
		if _, err := Parse("google-benchmark", strings.NewReader(in)); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: got %v, want %q", in, err, msg)
		}
	}
}

func TestPytestBenchmark(t *testing.T) {
	check(t, parseFile(t, "pytest-benchmark", "pytest-benchmark.json"), []want{
		{"test_parse.py", "test_parse_small", 1.2258500078132784e-05 * 1e9},
		{"test_parse.py", "test_sum", pytestMedian(t, 1)},
	})
	if _, err := Parse("pytest-benchmark", strings.NewReader(`{"machine_info": {}}`)); err == nil || !strings.Contains(err.Error(), "--benchmark-json") {
		t.Errorf("got %v", err)
	}
}

func pytestMedian(t *testing.T, i int) float64 {
	t.Helper()
	got := parseFile(t, "pytest-benchmark", "pytest-benchmark.json")
	return got[i].Value
}

func TestVitest(t *testing.T) {
	check(t, parseFile(t, "vitest", "vitest-bench.json"), []want{
		{"parse.bench.js", "sum", 0.0005290000000286454 * 1e6},
		{"parse.bench.js > json", "parse small", 0.002116000000000895 * 1e6},
	})
	got, err := Parse("vitest", strings.NewReader(`{"files": [{"groups": [{"fullName": "a", "benchmarks": [{"name": "b", "mean": 0.5}]}]}]}`))
	if err != nil || len(got) != 1 || got[0].Value != 0.5e6 {
		t.Errorf("mean only: %+v %v", got, err)
	}
	if _, err := Parse("vitest", strings.NewReader(`{"testResults": []}`)); err == nil || !strings.Contains(err.Error(), "--outputJson") {
		t.Errorf("got %v", err)
	}
}

func TestHarnessOutputAfterLogs(t *testing.T) {
	got, err := Parse("google-benchmark", strings.NewReader("Running ./bench\n{\n  \"benchmarks\": [{\"name\": \"a\", \"real_time\": 3, \"time_unit\": \"ns\"}]\n}\n"))
	if err != nil || len(got) != 1 {
		t.Errorf("got %+v %v", got, err)
	}
}

func TestCriterionChangeBlocksColourAndBencher(t *testing.T) {
	// A second run with throughput benchmarks: their change since the last
	// run comes as a block of its own, with a time: line in percent.
	ws := []want{
		{"", "work", 3926.4}, {"", "a very long benchmark name that will surely wrap around", 22.442},
		{"", "grp/op/10", 9.2514}, {"", "grp/op/100", 166.41}, {"", "tiny", 0.35045},
	}
	check(t, parseFile(t, "criterion", "criterion-throughput-change.txt"), ws)
	// --color always.
	got := parseFile(t, "criterion", "criterion-color.txt")
	if len(got) != 5 || got[0].Name != "work" || !near(got[0].Value, 3910.1) || got[1].Name != "a very long benchmark name that will surely wrap around" {
		t.Errorf("colour: %+v", got)
	}
	// --output-format bencher: whole nanoseconds, and the sub-nanosecond
	// benchmark, printed as 0, left out.
	check(t, parseFile(t, "criterion", "criterion-bencher.txt"), []want{
		{"", "work", 3754}, {"", "a very long benchmark name that will surely wrap around", 14},
		{"", "grp/op/10", 8}, {"", "grp/op/100", 168},
	})
}

func TestGoogleBenchmarkEdges(t *testing.T) {
	// A skipped benchmark, and a counter Google Benchmark writes as a bare
	// NaN, which isn't JSON.
	got, err := Parse("google-benchmark", strings.NewReader(`{"benchmarks": [
  {"name": "a", "run_name": "a", "run_type": "iteration", "real_time": 0, "time_unit": "ns", "skipped": true, "skip_message": "no AVX"},
  {"name": "b", "run_name": "b", "run_type": "iteration", "real_time": 5, "time_unit": "us", "items_per_second": NaN, "label": "NaN is fine in a string"},
  {"name": "c", "run_name": "c", "run_type": "iteration", "real_time": 7, "time_unit": "ns", "bytes_per_second": -Infinity}
]}`))
	if err != nil || len(got) != 2 || got[0].Name != "b" || got[0].Value != 5000 {
		t.Errorf("got %+v %v", got, err)
	}
	// --benchmark_report_aggregates_only: the median aggregate stands in.
	got, err = Parse("google-benchmark", strings.NewReader(`{"benchmarks": [
  {"name": "a_mean", "run_name": "a", "run_type": "aggregate", "aggregate_name": "mean", "real_time": 11, "time_unit": "ns"},
  {"name": "a_median", "run_name": "a", "run_type": "aggregate", "aggregate_name": "median", "real_time": 10, "time_unit": "ns"},
  {"name": "a_stddev", "run_name": "a", "run_type": "aggregate", "aggregate_name": "stddev", "real_time": 1, "time_unit": "ns"}
]}`))
	if err != nil || len(got) != 1 || got[0].Name != "a" || got[0].Value != 10 {
		t.Errorf("aggregates only: %+v %v", got, err)
	}
}

func TestOneValuePerBenchmarkHarnesses(t *testing.T) {
	_, err := Parse("vitest", strings.NewReader(`{"files": [{"groups": [
  {"fullName": "a > g", "benchmarks": [{"name": "x", "median": 1}]},
  {"fullName": "a > g", "benchmarks": [{"name": "x", "median": 2}]}]}]}`))
	if err == nil || !strings.Contains(err.Error(), "rename one") {
		t.Errorf("vitest duplicate: %v", err)
	}
	_, err = Parse("vitest", strings.NewReader(`{"files": [{"groups": [{"fullName": "g", "benchmarks": [{"name": "x", "median": 0, "mean": 3}]}]}]}`))
	if err == nil || !strings.Contains(err.Error(), "coarse") {
		t.Errorf("vitest zero median: %v", err)
	}
}
