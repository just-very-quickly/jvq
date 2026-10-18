// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"os"
	"strings"
	"testing"
)

func TestParseGoRealOutput(t *testing.T) {
	f, err := os.Open("testdata/go-real.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := ParseGo(f)
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ name, metric string }
	byKey := map[key]Sample{}
	for _, s := range got {
		byKey[key{s.Name, s.Metric}] = s
	}
	want := []struct {
		name, metric, unit string
		value              float64
	}{
		{"BenchmarkAlloc", Time, "ns/op", 33.53},
		{"BenchmarkAlloc", Bytes, "B/op", 64},
		{"BenchmarkAlloc", Allocs, "allocs/op", 1},
		{"BenchmarkSizes/n=10", Time, "ns/op", 15.55},
		{"BenchmarkSizes/n=10", "MB/s", "MB/s", 643.25},
		{"BenchmarkSizes/n=1000", Bytes, "B/op", 1024},
		{"BenchmarkCustom", "widgets/op", "widgets/op", 42},
		{"BenchmarkCustom", Time, "ns/op", 0.1308},
	}
	for _, w := range want {
		s, ok := byKey[key{w.name, w.metric}]
		if !ok {
			t.Errorf("missing %s %s", w.name, w.metric)
			continue
		}
		if s.Unit != w.unit || s.Value != w.value || s.Package != "example.com/gb" {
			t.Errorf("%s %s: got %+v", w.name, w.metric, s)
		}
	}
	if len(got) != 15 { // 3 + 4 + 4 + 4 metrics on four lines
		t.Errorf("got %d samples, want 15", len(got))
	}
	if k := byKey[key{"BenchmarkAlloc", Time}].Key(); k != "example.com/gb.BenchmarkAlloc" {
		t.Errorf("key %q", k)
	}
}

func TestParseGoSkipsNoise(t *testing.T) {
	in := strings.Join([]string{
		"=== RUN   TestX",
		"BenchmarkLog says hello",           // a log line starting with Benchmark
		"BenchmarkBad-8 notanumber 1 ns/op", // bad iteration count
		"BenchmarkOdd-8 10 1 ns/op 2",       // dangling value
		"BenchmarkBadValue-8 10 x ns/op",    // bad value
		"BenchmarkOK-8 10 5.5 ns/op",        // the one good line
		"--- FAIL: BenchmarkBroken",
		"FAIL",
	}, "\n")
	got, err := ParseGo(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "BenchmarkOK" || got[0].Value != 5.5 || got[0].Package != "" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Key() != "BenchmarkOK" {
		t.Errorf("key without package: %q", got[0].Key())
	}
}

func TestParseGoPackagesKeepSameNamesApart(t *testing.T) {
	in := "pkg: a\nBenchmarkX-4 10 1 ns/op\npkg: b\nBenchmarkX-4 10 2 ns/op\n"
	got, _ := ParseGo(strings.NewReader(in))
	if len(got) != 2 || got[0].Key() == got[1].Key() {
		t.Fatalf("got %+v", got)
	}
}

func TestParseGoNoProcsSuffix(t *testing.T) {
	// GOMAXPROCS=1 prints no suffix.
	got, _ := ParseGo(strings.NewReader("BenchmarkOne 10 3 ns/op\n"))
	if len(got) != 1 || got[0].Name != "BenchmarkOne" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseUnknownAdapter(t *testing.T) {
	if _, err := Parse("criterion", strings.NewReader("")); err == nil {
		t.Error("unknown adapter accepted")
	}
	if got, err := Parse("", strings.NewReader("BenchmarkA-2 1 1 ns/op\n")); err != nil || len(got) != 1 {
		t.Errorf("default adapter: %v %v", got, err)
	}
}

func BenchmarkParseGo(b *testing.B) {
	data, err := os.ReadFile("testdata/go-real.txt")
	if err != nil {
		b.Fatal(err)
	}
	big := strings.Repeat(string(data), 50)
	b.SetBytes(int64(len(big)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ParseGo(strings.NewReader(big)); err != nil {
			b.Fatal(err)
		}
	}
}

func TestProcsSuffixOnlyWhenShared(t *testing.T) {
	// GOMAXPROCS=1: no suffix, and a trailing number is part of the name.
	got, _ := ParseGo(strings.NewReader("BenchmarkSize-64 10 1 ns/op\nBenchmarkSize-128 10 2 ns/op\n"))
	if got[0].Name != "BenchmarkSize-64" || got[1].Name != "BenchmarkSize-128" {
		t.Errorf("names merged: %+v", got)
	}
	// -cpu=1,4: different suffixes keep the results apart.
	got, _ = ParseGo(strings.NewReader("BenchmarkA 10 1 ns/op\nBenchmarkA-4 10 2 ns/op\n"))
	if got[0].Name == got[1].Name {
		t.Errorf("-cpu results merged: %+v", got)
	}
	// The usual case: one shared suffix is dropped.
	got, _ = ParseGo(strings.NewReader("BenchmarkA-8 10 1 ns/op\nBenchmarkB/x-8 10 2 ns/op\n"))
	if got[0].Name != "BenchmarkA" || got[1].Name != "BenchmarkB/x" {
		t.Errorf("shared suffix kept: %+v", got)
	}
}
