// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Adapters for other languages' benchmark harnesses, each reading the
// harness's own output as it is. Each gives one time per benchmark per run,
// in nanoseconds; when a run repeats a benchmark (Google Benchmark's
// --benchmark_repetitions), the runner takes the median.

// timeScale converts a time unit as harnesses print it to nanoseconds.
func timeScale(u string) (float64, bool) {
	switch strings.TrimSpace(u) {
	case "ps":
		return 1e-3, true
	case "ns":
		return 1, true
	case "µs", "μs", "us":
		return 1e3, true
	case "ms":
		return 1e6, true
	case "s":
		return 1e9, true
	}
	return 0, false
}

// ParseCriterion reads what Rust's criterion prints on standard output
// during `cargo bench`:
//
//	parse/small             time:   [1.8615 µs 1.8732 µs 1.8884 µs]
//
// The middle value is criterion's estimate for the run. A long name goes on
// a line of its own, with "time:" on the next. The bencher format, which
// `cargo bench -- --output-format bencher` and Rust's built-in #[bench]
// print, works too:
//
//	test parse_small ... bench:       1,873 ns/iter (+/- 21)
func ParseCriterion(r io.Reader) ([]Sample, error) {
	var out []Sample
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	prev := ""
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimRight(ansi.ReplaceAllString(sc.Text(), ""), " \t\r")
		trimmed := strings.TrimSpace(line)
		if s, ok, err := parseBencher(trimmed); ok {
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
			if s.Value > 0 {
				out = append(out, s)
			}
		} else if i := strings.Index(line, "time:"); i >= 0 && strings.HasPrefix(strings.TrimSpace(line[i+5:]), "[") {
			if strings.TrimSpace(prev) == "change:" || strings.Contains(line[i:], "%") {
				// The change since criterion's last run, which a benchmark
				// with a throughput prints as its own block; jvq measures
				// base and head itself.
				prev = line
				continue
			}
			name := strings.TrimSpace(line[:i])
			if name == "" {
				name = strings.TrimSpace(prev)
			}
			if name == "" {
				return nil, fmt.Errorf("line %d: a criterion time with no benchmark name", n)
			}
			v, err := criterionEstimate(line[i+5:])
			if err != nil {
				return nil, fmt.Errorf("line %d (%s): %w", n, name, err)
			}
			out = append(out, Sample{Name: name, Metric: Time, Unit: "ns", Value: v})
		}
		if trimmed != "" {
			prev = line
		}
	}
	return out, sc.Err()
}

// ansi matches the colour codes criterion and cargo print with --color
// always, and the shift-in character some terminals' resets end with.
var ansi = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]|\x0f")

// criterionEstimate reads "[1.8615 µs 1.8732 µs 1.8884 µs]" and returns the
// middle value in nanoseconds.
func criterionEstimate(s string) (float64, error) {
	s = strings.TrimSpace(s)
	end := strings.Index(s, "]")
	if !strings.HasPrefix(s, "[") || end < 0 {
		return 0, errors.New("want [low estimate high] after time:")
	}
	f := strings.Fields(s[1:end])
	if len(f) != 6 {
		return 0, fmt.Errorf("want three values with units in %q", s[:end+1])
	}
	v, err := strconv.ParseFloat(f[2], 64)
	scale, ok := timeScale(f[3])
	if err != nil || !ok || v <= 0 || math.IsInf(v, 0) {
		return 0, fmt.Errorf("can't read the estimate %q %q", f[2], f[3])
	}
	return v * scale, nil
}

// parseBencher reads "test NAME ... bench:   1,234 ns/iter (+/- 56)".
func parseBencher(line string) (Sample, bool, error) {
	if !strings.HasPrefix(line, "test ") {
		return Sample{}, false, nil
	}
	i := strings.Index(line, " bench:")
	if i < 0 {
		return Sample{}, false, nil
	}
	name := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line[5:i]), "..."))
	f := strings.Fields(line[i+len(" bench:"):])
	if len(f) < 2 || !strings.HasSuffix(f[1], "/iter") {
		return Sample{}, true, fmt.Errorf("%s: want a time such as 1,234 ns/iter", name)
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(f[0], ",", ""), 64)
	scale, ok := timeScale(strings.TrimSuffix(f[1], "/iter"))
	if err != nil || !ok || v < 0 {
		return Sample{}, true, fmt.Errorf("%s: can't read the time %q", name, f[0]+" "+f[1])
	}
	// The bencher format rounds to whole units, so a benchmark under half a
	// nanosecond prints 0. That one is left out (it gives no ratio to
	// judge); criterion's default output keeps the fraction.
	return Sample{Name: name, Metric: Time, Unit: "ns", Value: v * scale}, true, nil
}

// jsonDocument returns the JSON document in data: all of it, or the first
// one that starts on a line of its own after some other output.
func jsonDocument(data []byte, format string) ([]byte, error) {
	t := bytes.TrimSpace(data)
	if len(t) == 0 {
		return nil, nil
	}
	if json.Valid(t) {
		return t, nil
	}
	if d := firstDocument(data); d != nil {
		return d, nil
	}
	var v any
	return nil, fmt.Errorf("%s: %w", format, jsonErr(json.Unmarshal(t, &v), t))
}

// ParseGoogleBenchmark reads Google Benchmark's JSON, from
// --benchmark_format=json or --benchmark_out with --benchmark_out_format=json.
// Each iteration entry gives a sample of its real (wall-clock) time;
// aggregates (mean, median, stddev) are skipped, since jvq works from the
// repetitions themselves.
func ParseGoogleBenchmark(r io.Reader) ([]Sample, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	doc, err := jsonDocument(nonFinite(data), "google-benchmark")
	if doc == nil || err != nil {
		return nil, err
	}
	var gb struct {
		Benchmarks *[]struct {
			Name          string   `json:"name"`
			RunName       string   `json:"run_name"`
			RunType       string   `json:"run_type"`
			AggregateName string   `json:"aggregate_name"`
			RealTime      *float64 `json:"real_time"`
			TimeUnit      string   `json:"time_unit"`
			ErrorOccurred bool     `json:"error_occurred"`
			Skipped       bool     `json:"skipped"`
			ErrorMessage  string   `json:"error_message"`
		} `json:"benchmarks"`
	}
	if err := json.Unmarshal(doc, &gb); err != nil || gb.Benchmarks == nil {
		return nil, errors.New(`google-benchmark: want Google Benchmark's JSON, with a "benchmarks" list; run the benchmark with --benchmark_format=json`)
	}
	// With --benchmark_report_aggregates_only there are no iteration
	// entries; then each benchmark's median aggregate stands for the run.
	iterations := false
	for _, b := range *gb.Benchmarks {
		if b.RunType != "aggregate" && b.AggregateName == "" {
			iterations = true
		}
	}
	var out []Sample
	for _, b := range *gb.Benchmarks {
		aggregate := b.RunType == "aggregate" || b.AggregateName != ""
		if aggregate && (iterations || b.AggregateName != "median") {
			continue
		}
		if b.Skipped {
			continue // SkipWithMessage: nothing measured
		}
		name := b.RunName
		if name == "" {
			name = b.Name
		}
		if b.ErrorOccurred {
			return nil, fmt.Errorf("google-benchmark: %s reported an error: %s", name, b.ErrorMessage)
		}
		unit := b.TimeUnit
		if unit == "" {
			unit = "ns"
		}
		scale, ok := timeScale(unit)
		if !ok || b.RealTime == nil || *b.RealTime <= 0 {
			return nil, fmt.Errorf("google-benchmark: %s has no usable real_time", name)
		}
		out = append(out, Sample{Name: name, Metric: Time, Unit: "ns", Value: *b.RealTime * scale})
	}
	return out, nil
}

// nonFinite turns the bare NaN and Infinity that Google Benchmark writes
// for some counters, which aren't JSON, into null, outside strings.
func nonFinite(b []byte) []byte {
	if !bytes.Contains(b, []byte("NaN")) && !bytes.Contains(b, []byte("Infinity")) {
		return b
	}
	out := make([]byte, 0, len(b))
	inString, escaped := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		rest := b[i:]
		switch {
		case bytes.HasPrefix(rest, []byte("-Infinity")):
			out = append(out, "null"...)
			i += len("-Infinity") - 1
		case bytes.HasPrefix(rest, []byte("Infinity")):
			out = append(out, "null"...)
			i += len("Infinity") - 1
		case bytes.HasPrefix(rest, []byte("NaN")):
			out = append(out, "null"...)
			i += len("NaN") - 1
		default:
			out = append(out, c)
		}
	}
	return out
}

// noDuplicates fails when one run reports two benchmarks under one key:
// pytest-benchmark and vitest give exactly one value per benchmark, so a
// repeat is two different benchmarks that would be merged.
func noDuplicates(format string, samples []Sample) error {
	seen := map[string]bool{}
	for _, s := range samples {
		if seen[s.Key()] {
			return fmt.Errorf("%s: two benchmarks are both called %q; rename one so jvq can tell them apart", format, s.Key())
		}
		seen[s.Key()] = true
	}
	return nil
}

// ParsePytestBenchmark reads pytest-benchmark's JSON, from
// --benchmark-json=FILE. Each benchmark gives its median time for the run;
// the file's name and any test class keep tests of the same name apart.
func ParsePytestBenchmark(r io.Reader) ([]Sample, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	doc, err := jsonDocument(data, "pytest-benchmark")
	if doc == nil || err != nil {
		return nil, err
	}
	var pb struct {
		Benchmarks *[]struct {
			Name     string `json:"name"`
			Fullname string `json:"fullname"`
			Stats    struct {
				Median *float64 `json:"median"`
			} `json:"stats"`
		} `json:"benchmarks"`
	}
	if err := json.Unmarshal(doc, &pb); err != nil || pb.Benchmarks == nil {
		return nil, errors.New(`pytest-benchmark: want pytest-benchmark's JSON, with a "benchmarks" list; run pytest with --benchmark-json=FILE and set output to FILE`)
	}
	var out []Sample
	for _, b := range *pb.Benchmarks {
		if b.Stats.Median == nil || *b.Stats.Median <= 0 {
			return nil, fmt.Errorf("pytest-benchmark: %s has no median time", b.Name)
		}
		group := strings.TrimSuffix(b.Fullname, "::"+b.Name)
		if group == b.Fullname {
			group = ""
		}
		out = append(out, Sample{Package: group, Name: b.Name, Metric: Time, Unit: "ns", Value: *b.Stats.Median * 1e9})
	}
	return out, noDuplicates("pytest-benchmark", out)
}

// ParseVitest reads the JSON report of `vitest bench --outputJson FILE`.
// Each benchmark gives its median time for the run (the mean, in versions
// that don't report a median); the file and describe blocks around it, as
// vitest names them, keep equal names apart.
func ParseVitest(r io.Reader) ([]Sample, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	doc, err := jsonDocument(data, "vitest")
	if doc == nil || err != nil {
		return nil, err
	}
	var vt struct {
		Files *[]struct {
			Groups []struct {
				FullName   string `json:"fullName"`
				Benchmarks []struct {
					Name   string   `json:"name"`
					Median *float64 `json:"median"`
					Mean   *float64 `json:"mean"`
				} `json:"benchmarks"`
			} `json:"groups"`
		} `json:"files"`
	}
	if err := json.Unmarshal(doc, &vt); err != nil || vt.Files == nil {
		return nil, errors.New(`vitest: want the JSON report of vitest bench, with a "files" list; run vitest bench --run --outputJson FILE and set output to FILE (vitest 1 to 4; vitest 5 dropped --outputJson)`)
	}
	var out []Sample
	for _, f := range *vt.Files {
		for _, g := range f.Groups {
			for _, b := range g.Benchmarks {
				// vitest 1 has no median; later versions always do. The
				// mean is used only when the median is missing, so one
				// benchmark never mixes the two.
				v := b.Median
				if v == nil {
					v = b.Mean
				}
				if v == nil || *v <= 0 {
					return nil, fmt.Errorf("vitest: %s has no time above 0 (the timer may be too coarse for it)", b.Name)
				}
				out = append(out, Sample{Package: g.FullName, Name: b.Name, Metric: Time, Unit: "ns", Value: *v * 1e6})
			}
		}
	}
	return out, noDuplicates("vitest", out)
}
