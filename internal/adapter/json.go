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
// bytes) is bytes; allocs is allocations. Spelled-out names (seconds,
// milliseconds) work too, and a "/op" ending is allowed on all of them. Any
// other unit is a custom metric, reported but not gated. In the line form,
// an object with none of a result's fields is skipped, such as a JSON log
// line; and a document printed after some other text is found too.

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
	samples, lineErr := parseJSONLines(data)
	if lineErr == nil && len(samples) > 0 {
		return samples, nil
	}
	// Or one document, perhaps pretty-printed, after some other lines.
	if doc := firstDocument(data); doc != nil {
		return parseJSONDoc(doc)
	}
	if lineErr == nil && (trimmed[0] == '[' || trimmed[0] == '{') {
		// It looks like one document, and it isn't valid JSON.
		var v any
		return nil, jsonErr(json.Unmarshal(trimmed, &v), trimmed)
	}
	return samples, lineErr
}

// firstDocument finds the first line that starts with { or [ and returns
// the JSON value that starts there, if it is one, with any text after it
// left out.
func firstDocument(data []byte) []byte {
	for off := 0; off < len(data); {
		end := bytes.IndexByte(data[off:], '\n')
		line := data[off:]
		if end >= 0 {
			line = data[off : off+end]
		}
		t := bytes.TrimLeft(line, " \t\r")
		if len(t) > 0 && (t[0] == '{' || t[0] == '[') {
			start := off + (len(line) - len(t))
			dec := json.NewDecoder(bytes.NewReader(data[start:]))
			var raw json.RawMessage
			if dec.Decode(&raw) == nil {
				return raw
			}
			return nil
		}
		if end < 0 {
			break
		}
		off += end + 1
	}
	return nil
}

// resultKeys are the fields a result may have. A line whose object has
// none of them (or "results") is someone else's JSON, such as a log line.
var resultKeys = map[string]bool{"name": true, "group": true, "unit": true, "value": true, "values": true}

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
		var obj map[string]json.RawMessage
		if json.Unmarshal(line, &obj) == nil && !looksLikeResults(obj) {
			continue // a structured log line, not a result
		}
		s, err := parseJSONDoc(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		out = append(out, s...)
	}
	return out, sc.Err()
}

func looksLikeResults(obj map[string]json.RawMessage) bool {
	if _, ok := obj["results"]; ok {
		return true
	}
	for k := range obj {
		if resultKeys[k] {
			return true
		}
	}
	return false
}

// parseJSONDoc reads one result object, a {"results": [...]} document, or
// an array of results.
func parseJSONDoc(b []byte) ([]Sample, error) {
	switch b[0] {
	case '[':
		var raw []json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, jsonErr(err, b)
		}
		return parseJSONResults(raw)
	case '{':
	default:
		return nil, errors.New(`want a result object, an array of results, or {"results": [...]}`)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, jsonErr(err, b)
	}
	if rs, ok := doc["results"]; ok {
		var raw []json.RawMessage
		if err := json.Unmarshal(rs, &raw); err != nil {
			return nil, fmt.Errorf(`"results" must be an array of results: %w`, jsonErr(err, rs))
		}
		return parseJSONResults(raw)
	}
	return parseJSONResults([]json.RawMessage{b})
}

func parseJSONResults(raw []json.RawMessage) ([]Sample, error) {
	var out []Sample
	for _, m := range raw {
		// Go matches JSON keys without regard to case; a script's author
		// expects "Value" to be a different key from "value", so check the
		// keys exactly first.
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(m, &obj); err != nil {
			return nil, fmt.Errorf("a result must be an object: %w", jsonErr(err, m))
		}
		for k := range obj {
			if !resultKeys[k] {
				return nil, fmt.Errorf("a result has an unknown field %q (results have name, group, unit, value or values)", k)
			}
		}
		if v, ok := obj["value"]; ok && string(bytes.TrimSpace(v)) == "null" {
			return nil, errors.New("a result has a null value (JavaScript's JSON.stringify writes NaN and Infinity as null)")
		}
		var r jsonResult
		if err := json.Unmarshal(m, &r); err != nil {
			return nil, fmt.Errorf("a result: %w", jsonErr(err, m))
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
			return nil, fmt.Errorf("%s has a null value (JavaScript's JSON.stringify writes NaN and Infinity as null)", where)
		}
		x := *v * scale
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("%s has a value that isn't a finite number", where)
		}
		if x < 0 && (metric == Time || metric == Bytes || metric == Allocs || metric == Instructions) {
			return nil, fmt.Errorf("%s has a negative %s", where, metric)
		}
		if x == 0 && metric == Time {
			return nil, fmt.Errorf("%s has a time of 0; time a batch long enough for the clock to see it", where)
		}
		if x == 0 && metric == Instructions {
			return nil, fmt.Errorf("%s has an instruction count of 0", where)
		}
		out = append(out, Sample{Package: r.Group, Name: r.Name, Metric: metric, Unit: unit, Value: x})
	}
	return out, nil
}

// jsonUnit maps a unit to its metric, the unit stored, and the factor that
// converts the value to it.
func jsonUnit(u string) (metric, unit string, scale float64) {
	base := strings.TrimSuffix(strings.TrimSpace(u), "/op")
	switch strings.ToLower(base) {
	case "ns", "nanosecond", "nanoseconds":
		return Time, "ns", 1
	case "us", "µs", "μs", "microsecond", "microseconds":
		return Time, "ns", 1e3
	case "ms", "millisecond", "milliseconds":
		if base == "Ms" || base == "MS" {
			break // a megasecond, or a typo; don't guess
		}
		return Time, "ns", 1e6
	case "s", "sec", "secs", "second", "seconds":
		return Time, "ns", 1e9
	case "b", "byte", "bytes":
		if base == "b" {
			break // bits, perhaps
		}
		return Bytes, "B", 1
	case "allocs", "alloc", "allocations":
		return Allocs, "allocs", 1
	case "instructions", "instruction", "instrs", "inst":
		return Instructions, "instructions", 1
	}
	return u, u, 1
}

func jsonErr(err error, input []byte) error {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		msg := fmt.Sprintf("not valid JSON (at byte %d): %v", se.Offset, err)
		switch {
		case bytes.Contains(input, []byte("NaN")) || bytes.Contains(input, []byte("Infinity")):
			msg += "; NaN and Infinity aren't JSON"
		case bytes.Contains(input, []byte("'")):
			msg += "; JSON strings take double quotes (in Python, print json.dumps(...), not a dict)"
		}
		return errors.New(msg)
	}
	return err
}
