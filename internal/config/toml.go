// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// parseTOML reads the subset of TOML that jvq.toml needs, into nested maps:
//
//   - comments (#) and blank lines
//   - tables: [budget], [budget.bench."BenchmarkParse*"] (bare or quoted parts)
//   - key = value, where the key is bare (A-Za-z0-9_-) or quoted
//   - values: "basic strings" with \" \\ \n \t \r escapes, 'literal strings',
//     integers, floats, true and false, and arrays of those, which may span
//     lines
//
// Anything else (inline tables, dates, dotted keys, multi-line strings,
// arrays of tables) is an error that names the line, rather than a guess.
// A key set twice and a table opened twice are errors too.
func parseTOML(src string) (map[string]any, error) {
	root := map[string]any{}
	cur := root
	opened := map[string]bool{}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		lineNo := i + 1
		line := strings.TrimSpace(stripComment(lines[i]))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if strings.HasPrefix(line, "[[") {
				return nil, fmt.Errorf("line %d: arrays of tables aren't supported", lineNo)
			}
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: table header needs a closing ]", lineNo)
			}
			parts, err := splitKey(strings.TrimSpace(line[1 : len(line)-1]))
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", lineNo, err)
			}
			id := strings.Join(parts, "\x00")
			if opened[id] {
				return nil, fmt.Errorf("line %d: table [%s] appears twice", lineNo, strings.Join(parts, "."))
			}
			opened[id] = true
			t := root
			for _, p := range parts {
				next, ok := t[p]
				if !ok {
					m := map[string]any{}
					t[p] = m
					t = m
					continue
				}
				m, ok := next.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("line %d: %q is already a value, not a table", lineNo, p)
				}
				t = m
			}
			cur = t
			continue
		}
		eq := indexOutsideQuotes(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("line %d: expected key = value", lineNo)
		}
		keyParts, err := splitKey(strings.TrimSpace(line[:eq]))
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", lineNo, err)
		}
		if len(keyParts) != 1 {
			return nil, fmt.Errorf("line %d: dotted keys aren't supported; use a [table]", lineNo)
		}
		key := keyParts[0]
		raw := strings.TrimSpace(line[eq+1:])
		// An array may continue over the next lines until its brackets close.
		if strings.HasPrefix(raw, "[") {
			for depth(raw) > 0 {
				i++
				if i >= len(lines) {
					return nil, fmt.Errorf("line %d: array is never closed", lineNo)
				}
				raw += " " + strings.TrimSpace(stripComment(lines[i]))
			}
		}
		v, rest, err := parseValue(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", lineNo, err)
		}
		if strings.TrimSpace(rest) != "" {
			return nil, fmt.Errorf("line %d: unexpected %q after the value", lineNo, strings.TrimSpace(rest))
		}
		if _, dup := cur[key]; dup {
			return nil, fmt.Errorf("line %d: %q is set twice", lineNo, key)
		}
		cur[key] = v
	}
	return root, nil
}

// stripComment cuts a # comment that isn't inside a string.
func stripComment(s string) string {
	if i := indexOutsideQuotes(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

// indexOutsideQuotes finds c outside "..." and '...' strings.
func indexOutsideQuotes(s string, c byte) int {
	var q byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case q == '"' && ch == '\\':
			i++
		case q != 0 && ch == q:
			q = 0
		case q == 0 && (ch == '"' || ch == '\''):
			q = ch
		case q == 0 && ch == c:
			return i
		}
	}
	return -1
}

// depth is how many [ are still open in s, ignoring brackets inside strings.
func depth(s string) int {
	d := 0
	var q byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case q == '"' && ch == '\\':
			i++
		case q != 0 && ch == q:
			q = 0
		case q == 0 && (ch == '"' || ch == '\''):
			q = ch
		case q == 0 && ch == '[':
			d++
		case q == 0 && ch == ']':
			d--
		}
	}
	return d
}

func isBare(r rune) bool {
	return r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-')
}

// splitKey splits a.b."c.d" into its parts.
func splitKey(s string) ([]string, error) {
	var parts []string
	for {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, fmt.Errorf("empty key")
		}
		var part string
		switch s[0] {
		case '"', '\'':
			v, rest, err := parseString(s)
			if err != nil {
				return nil, err
			}
			part, s = v, rest
		default:
			j := 0
			for j < len(s) && isBare(rune(s[j])) {
				j++
			}
			if j == 0 {
				return nil, fmt.Errorf("bad key %q", s)
			}
			part, s = s[:j], s[j:]
		}
		parts = append(parts, part)
		s = strings.TrimSpace(s)
		if s == "" {
			return parts, nil
		}
		if s[0] != '.' {
			return nil, fmt.Errorf("bad key near %q", s)
		}
		s = s[1:]
	}
}

func parseValue(s string) (any, string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, "", fmt.Errorf("missing value")
	}
	switch s[0] {
	case '"', '\'':
		if strings.HasPrefix(s, `"""`) || strings.HasPrefix(s, `'''`) {
			return nil, "", fmt.Errorf("multi-line strings aren't supported")
		}
		return parseString(s)
	case '[':
		return parseArray(s)
	case '{':
		return nil, "", fmt.Errorf("inline tables aren't supported; use a [table]")
	}
	j := 0
	for j < len(s) && s[j] != ',' && s[j] != ']' && s[j] != ' ' && s[j] != '\t' {
		j++
	}
	tok, rest := s[:j], s[j:]
	switch tok {
	case "true":
		return true, rest, nil
	case "false":
		return false, rest, nil
	}
	clean := strings.ReplaceAll(tok, "_", "")
	if n, err := strconv.ParseInt(clean, 10, 64); err == nil {
		return n, rest, nil
	}
	if f, err := strconv.ParseFloat(clean, 64); err == nil && !strings.ContainsAny(clean, "xXnN") {
		return f, rest, nil
	}
	return nil, "", fmt.Errorf("can't read value %q (strings need quotes)", tok)
}

func parseString(s string) (string, string, error) {
	q := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		ch := s[i]
		if ch == q {
			return b.String(), s[i+1:], nil
		}
		if q == '"' && ch == '\\' {
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				return "", "", fmt.Errorf("unsupported escape \\%c", s[i])
			}
			continue
		}
		b.WriteByte(ch)
	}
	return "", "", fmt.Errorf("string is never closed")
}

func parseArray(s string) ([]any, string, error) {
	s = strings.TrimSpace(s[1:])
	var out []any
	for {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "]") {
			return out, s[1:], nil
		}
		if s == "" {
			return nil, "", fmt.Errorf("array is never closed")
		}
		v, rest, err := parseValue(s)
		if err != nil {
			return nil, "", err
		}
		if _, nested := v.([]any); nested {
			return nil, "", fmt.Errorf("nested arrays aren't supported")
		}
		out = append(out, v)
		s = strings.TrimSpace(rest)
		if strings.HasPrefix(s, ",") {
			s = s[1:]
			continue
		}
		if !strings.HasPrefix(s, "]") {
			return nil, "", fmt.Errorf("expected , or ] in array near %q", s)
		}
	}
}
