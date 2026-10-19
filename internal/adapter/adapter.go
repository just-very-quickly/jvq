// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package adapter turns the output of a benchmark command into samples.
//
// The go adapter reads the standard Go benchmark format, the one
// `go test -bench` prints:
//
//	pkg: example.com/parse
//	BenchmarkParse/small-8   	 1234567	       912.3 ns/op	     128 B/op	       3 allocs/op
//
// Every result line becomes one sample per metric on it. The json adapter
// reads jvq's own small JSON format (json.go), and the time adapter reads
// nothing: the runner times the whole command (TimeSample).
package adapter

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Metric names jvq knows how to judge. Anything else on a result line is
// kept as a custom metric and reported, not gated.
const (
	Time   = "time"   // ns/op, stored in nanoseconds; lower is better
	Bytes  = "bytes"  // B/op; lower is better
	Allocs = "allocs" // allocs/op; lower is better
)

// Sample is one measured value of one metric of one benchmark.
type Sample struct {
	Package string  // the Go package path from the last "pkg:" line, if any
	Name    string  // benchmark name without the -GOMAXPROCS suffix
	Metric  string  // Time, Bytes, Allocs, or the unit itself for custom metrics
	Unit    string  // the unit as printed, such as "ns/op"
	Value   float64 // the value in that unit
}

// Key identifies a benchmark across runs: package and name.
func (s Sample) Key() string {
	if s.Package == "" {
		return s.Name
	}
	return s.Package + "." + s.Name
}

// Names lists the adapters jvq has.
var Names = []string{"go", "json", "time"}

// TimeName is the benchmark name the time adapter reports.
const TimeName = "command"

// Known reports whether jvq has the named adapter.
func Known(name string) bool {
	for _, n := range Names {
		if n == name {
			return true
		}
	}
	return false
}

// Timed reports whether the adapter measures the command's own run time
// instead of reading its output.
func Timed(name string) bool { return name == "time" }

// TimeSample is the time adapter's one sample for a run that took d.
func TimeSample(d time.Duration) Sample {
	return Sample{Name: TimeName, Metric: Time, Unit: "ns", Value: float64(d.Nanoseconds())}
}

// Parse reads output in the named adapter's format.
func Parse(adapter string, r io.Reader) ([]Sample, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if data, err = utf8Text(data); err != nil {
		return nil, err
	}
	r = bytes.NewReader(data)
	switch adapter {
	case "go", "":
		return ParseGo(r)
	case "json":
		return ParseJSON(r)
	case "time":
		return nil, fmt.Errorf("the time adapter reads no output; jvq times the command itself")
	default:
		return nil, fmt.Errorf("unknown adapter %q (jvq has: %s)", adapter, strings.Join(Names, ", "))
	}
}

// utf8Text drops a UTF-8 byte order mark, which some editors and Windows
// tools write, and refuses UTF-16 (Windows PowerShell 5.1's > writes it)
// with a message that says what to do.
func utf8Text(b []byte) ([]byte, error) {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return b[3:], nil
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}), bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return nil, errors.New("the output is UTF-16; write it as UTF-8 (in PowerShell, pipe to Out-File -Encoding utf8, or use PowerShell 7)")
	}
	return b, nil
}

var procsSuffix = regexp.MustCompile(`-[0-9]+$`)

// ParseGo reads Go benchmark output. Lines that aren't results (PASS, ok,
// goos:, test logs) are skipped. A result line must have the benchmark name,
// an iteration count, and value-unit pairs.
func ParseGo(r io.Reader) ([]Sample, error) {
	var out []Sample
	pkg := ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "pkg:") {
			pkg = strings.TrimSpace(strings.TrimPrefix(line, "pkg:"))
			continue
		}
		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}
		f := strings.Fields(line)
		// name, iterations, then at least one value-unit pair
		if len(f) < 4 || (len(f)-2)%2 != 0 {
			continue
		}
		if _, err := strconv.ParseUint(f[1], 10, 64); err != nil {
			continue
		}
		name := f[0]
		var row []Sample
		ok := true
		for i := 2; i+1 < len(f); i += 2 {
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				ok = false
				break
			}
			unit := f[i+1]
			s := Sample{Package: pkg, Name: name, Unit: unit, Value: v}
			switch unit {
			case "ns/op":
				s.Metric = Time
			case "B/op":
				s.Metric = Bytes
			case "allocs/op":
				s.Metric = Allocs
			default:
				s.Metric = unit
			}
			row = append(row, s)
		}
		if ok {
			out = append(out, row...)
		}
	}
	return stripProcs(out), sc.Err()
}

// stripProcs drops the -GOMAXPROCS suffix Go adds to benchmark names, such
// as the -8 in BenchmarkParse-8, but only when every result line carries
// the same one. With GOMAXPROCS=1 Go prints no suffix, and a name such as
// BenchmarkSize-64 must then stay whole; with -cpu=1,4 the suffixes differ
// and are what tells the results apart.
func stripProcs(samples []Sample) []Sample {
	suffix := ""
	for i, s := range samples {
		m := procsSuffix.FindString(s.Name)
		if m == "" || (i > 0 && m != suffix) {
			return samples
		}
		suffix = m
	}
	for i := range samples {
		samples[i].Name = strings.TrimSuffix(samples[i].Name, suffix)
	}
	return samples
}
