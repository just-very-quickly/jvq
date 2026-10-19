// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"strings"
	"testing"
	"time"
)

func parseJSON(t *testing.T, s string) []Sample {
	t.Helper()
	got, err := Parse("json", strings.NewReader(s))
	if err != nil {
		t.Fatalf("%v\ninput:\n%s", err, s)
	}
	return got
}

func TestJSONDocument(t *testing.T) {
	got := parseJSON(t, `{
  "results": [
    {"name": "parse/small", "group": "parser", "unit": "us", "values": [1.5, 2]},
    {"name": "parse/small", "group": "parser", "unit": "B/op", "value": 128},
    {"name": "parse/small", "group": "parser", "unit": "allocs", "value": 3},
    {"name": "throughput", "unit": "MB/s", "value": 640.5}
  ]
}`)
	if len(got) != 5 {
		t.Fatalf("got %d samples: %+v", len(got), got)
	}
	want := []Sample{
		{Package: "parser", Name: "parse/small", Metric: Time, Unit: "ns", Value: 1500},
		{Package: "parser", Name: "parse/small", Metric: Time, Unit: "ns", Value: 2000},
		{Package: "parser", Name: "parse/small", Metric: Bytes, Unit: "B", Value: 128},
		{Package: "parser", Name: "parse/small", Metric: Allocs, Unit: "allocs", Value: 3},
		{Name: "throughput", Metric: "MB/s", Unit: "MB/s", Value: 640.5},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("sample %d: %+v, want %+v", i, got[i], w)
		}
	}
	if got[0].Key() != "parser.parse/small" {
		t.Errorf("key %q", got[0].Key())
	}
}

func TestJSONUnits(t *testing.T) {
	for unit, ns := range map[string]float64{"ns": 2, "ns/op": 2, "us": 2e3, "µs": 2e3, "ms": 2e6, "s": 2e9} {
		got := parseJSON(t, `{"name": "a", "unit": "`+unit+`", "value": 2}`)
		if len(got) != 1 || got[0].Metric != Time || got[0].Value != ns {
			t.Errorf("unit %s: %+v", unit, got)
		}
	}
	if got := parseJSON(t, `{"name": "a", "unit": "bytes", "value": 2}`); got[0].Metric != Bytes {
		t.Errorf("bytes: %+v", got)
	}
}

func TestJSONArrayAndLines(t *testing.T) {
	if got := parseJSON(t, `[{"name": "a", "unit": "ns", "value": 1}, {"name": "b", "unit": "ns", "value": 2}]`); len(got) != 2 {
		t.Errorf("array: %+v", got)
	}
	got := parseJSON(t, "starting\n{\"name\": \"a\", \"unit\": \"ms\", \"value\": 1}\nwarm-up done\n"+
		"  {\"name\": \"a\", \"unit\": \"ms\", \"value\": 1.1}\n{\"results\": [{\"name\": \"b\", \"unit\": \"ns\", \"value\": 5}]}\nbye\n")
	if len(got) != 3 || got[1].Value != 1.1e6 || got[2].Name != "b" {
		t.Errorf("lines: %+v", got)
	}
	if got := parseJSON(t, "just text\n"); len(got) != 0 {
		t.Errorf("text: %+v", got)
	}
	if got := parseJSON(t, "  \n"); len(got) != 0 {
		t.Errorf("empty: %+v", got)
	}
}

func TestJSONErrorsNameTheProblem(t *testing.T) {
	cases := map[string]string{
		`{"name": "a", "value": 1}`:                              `no "unit"`,
		`{"unit": "ns", "value": 1}`:                             `no "name"`,
		`{"name": "a", "unit": "ns"}`:                            `no "value"`,
		`{"name": "a", "unit": "ns", "value": 1, "values": [2]}`: "both",
		`{"name": "a", "unit": "ns", "value": -1}`:               "negative time",
		`{"name": "a", "unit": "ns", "values": [1, null]}`:       "null",
		`{"name": "a", "unit": "ns", "value": "fast"}`:           "cannot unmarshal",
		`{"name": "a", "unit": "ns", "value": 1, "vlaue": 2}`:    `unknown field "vlaue"`,
		`{"results": {"name": "a"}}`:                             `"results" must be an array`,
		"{\n  \"results\": [\n    {\"name\": \"a\",}\n  ]\n}":    "not valid JSON",
		"ok\n{\"name\": \"a\", \"unit\": \"ns\" \"value\": 1}\n": "line 2",
		`[{"name": "a", "unit": "ns", "value": 1},]`:             "not valid JSON",
	}
	for in, want := range cases {
		_, err := Parse("json", strings.NewReader(in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", in, err, want)
		}
	}
}

func TestTimeAdapter(t *testing.T) {
	if !Timed("time") || Timed("go") || Timed("json") {
		t.Error("Timed")
	}
	s := TimeSample(1500 * time.Millisecond)
	if s.Name != TimeName || s.Metric != Time || s.Value != 1.5e9 {
		t.Errorf("sample %+v", s)
	}
	if _, err := Parse("time", strings.NewReader("")); err == nil {
		t.Error("the time adapter parsed output")
	}
	for _, n := range []string{"go", "json", "time"} {
		if !Known(n) {
			t.Errorf("%s not known", n)
		}
	}
	if Known("criterion") {
		t.Error("criterion known")
	}
}
