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
	"strings"
)

// The json adapter reads jvq's own small format, so any language can feed
// jvq by printing a few lines. A result is an object:
//
//	{"name": "parse/small", "unit": "ns", "value": 912.3}
//
// with an optional "group" (kept apart like a Go package) and either
// "value" (one sample) or "values" (several). The output is one document
// {"results": [...]}, or a bare array of results, or one result per line
// (JSON Lines). In the line form, lines that don't start with "{" are
// skipped, so the command may print other things too.
//
// Units: ns, us (or µs), ms and s are time, stored in nanoseconds; B (or
// bytes) is bytes; allocs is allocations. A "/op" ending is allowed on all
// of them. Any other unit is a custom metric, reported but not gated.

type jsonResult struct {
	Name   string     `json:"name"`
	Group  string     `json:"group"`
	Unit   string     `json:"unit"`
	Value  *float64   `json:"value"`
	Values []*float64 `json:"values"`
}

// ParseJSON reads the json adapter's format.
func ParseJSON(r io.Reader) ([]Sample, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	// One document: an object with "results", a result, or an array.
	if json.Valid(trimmed) {
		return parseJSONDoc(trimmed)
	}
	// Otherwise one result per line, with other lines around them.
	if trimmed[0] == '[' {
		var v any
		return nil, jsonErr(json.Unmarshal(trimmed, &v))
	}
	samples, err := parseJSONLines(data)
	if err != nil && trimmed[0] == '{' && !json.Valid(firstLine(trimmed)) {
		// It looks like one document that isn't valid JSON; say that,
		// rather than complaining about its first line.
		var v any
		return nil, jsonErr(json.Unmarshal(trimmed, &v))
	}
	return samples, err
}

func firstLine(b []byte) []byte {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return bytes.TrimSpace(b[:i])
	}
	return b
}

func parseJSONLines(data []byte) ([]Sample, error) {
	var out []Sample
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		s, err := parseJSONDoc(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		out = append(out, s...)
	}
	return out, sc.Err()
}

// parseJSONDoc reads one result object, a {"results": [...]} document, or
// an array of results.
func parseJSONDoc(b []byte) ([]Sample, error) {
	if b[0] == '[' {
		var raw []json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, jsonErr(err)
		}
		return parseJSONResults(raw)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, jsonErr(err)
	}
	if rs, ok := doc["results"]; ok {
		var raw []json.RawMessage
		if err := json.Unmarshal(rs, &raw); err != nil {
			return nil, fmt.Errorf(`"results" must be an array of results: %w`, jsonErr(err))
		}
		return parseJSONResults(raw)
	}
	return parseJSONResults([]json.RawMessage{b})
}

func parseJSONResults(raw []json.RawMessage) ([]Sample, error) {
	var out []Sample
	for _, m := range raw {
		dec := json.NewDecoder(bytes.NewReader(m))
		dec.DisallowUnknownFields()
		var r jsonResult
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("a result: %w", jsonErr(err))
		}
		s, err := r.samples()
		if err != nil {
			return nil, err
		}
		out = append(out, s...)
	}
	return out, nil
}

func (r jsonResult) samples() ([]Sample, error) {
	if strings.TrimSpace(r.Name) == "" {
		return nil, errors.New(`a result has no "name"`)
	}
	where := fmt.Sprintf("result %q", r.Name)
	if r.Unit == "" {
		return nil, fmt.Errorf(`%s has no "unit"`, where)
	}
	vals := r.Values
	switch {
	case r.Value != nil && r.Values != nil:
		return nil, fmt.Errorf(`%s has both "value" and "values"; give one`, where)
	case r.Value != nil:
		vals = []*float64{r.Value}
	case len(r.Values) == 0:
		return nil, fmt.Errorf(`%s has no "value"`, where)
	}
	metric, unit, scale := jsonUnit(r.Unit)
	out := make([]Sample, 0, len(vals))
	for _, v := range vals {
		if v == nil {
			return nil, fmt.Errorf("%s has a null value", where)
		}
		x := *v * scale
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("%s has a value that isn't a finite number", where)
		}
		if x < 0 && (metric == Time || metric == Bytes || metric == Allocs) {
			return nil, fmt.Errorf("%s has a negative %s", where, metric)
		}
		out = append(out, Sample{Package: r.Group, Name: r.Name, Metric: metric, Unit: unit, Value: x})
	}
	return out, nil
}

// jsonUnit maps a unit to its metric, the unit stored, and the factor that
// converts the value to it.
func jsonUnit(u string) (metric, unit string, scale float64) {
	base := strings.TrimSuffix(strings.TrimSpace(u), "/op")
	switch base {
	case "ns":
		return Time, "ns", 1
	case "us", "µs", "μs":
		return Time, "ns", 1e3
	case "ms":
		return Time, "ns", 1e6
	case "s":
		return Time, "ns", 1e9
	case "B", "bytes":
		return Bytes, "B", 1
	case "allocs":
		return Allocs, "allocs", 1
	}
	return u, u, 1
}

func jsonErr(err error) error {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return fmt.Errorf("not valid JSON (at byte %d): %v", se.Offset, err)
	}
	return err
}
