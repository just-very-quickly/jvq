// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package config reads jvq.toml and holds the settings for a run.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/just-very-quickly/jvq/internal/adapter"
)

// FileName is the config file jvq looks for.
const FileName = "jvq.toml"

// DefaultGoCommand is what jvq runs in a Go module when no command is given.
const DefaultGoCommand = "go test -run=NONE -bench=. -benchmem ./..."

// Config is everything a run needs, from jvq.toml, flags and defaults.
type Config struct {
	Command            string
	Adapter            string
	Build              string
	Output             string   // a file the command writes its results to, instead of stdout
	Size               []string // files (glob patterns) whose size is gated, measured after the build
	Rounds             int      // rounds per side to start with
	MaxRounds          int      // the most rounds per side while results are undecided
	Warmup             bool
	Confidence         float64
	Timeout            time.Duration // per command run
	FailOnInconclusive bool
	Budget             Budgets        // the defaults
	Bench              []BenchBudgets // per-benchmark overrides, in file order
	Ignore             []string       // benchmark name patterns left out
	Source             string         // the file it came from, or "" for defaults
}

// Budgets holds a budget per metric.
type Budgets struct {
	Time   Budget
	Bytes  Budget
	Allocs Budget
	Size   Budget
}

// BenchBudgets overrides budgets for benchmarks whose name matches Pattern.
// Only the metrics set in the table are overridden.
type BenchBudgets struct {
	Pattern string
	Time    *Budget
	Bytes   *Budget
	Allocs  *Budget
	Size    *Budget
}

// Budget is how much worse a metric may get before jvq calls it a
// regression: a fraction of the base value (Pct), or an absolute amount in
// the metric's unit (Abs). An Off budget means the metric is reported but
// never gated.
type Budget struct {
	Off bool
	Pct bool
	Val float64 // 0.05 for "5%", or the absolute amount
	Raw string  // as written, for reports
}

func (b Budget) String() string {
	if b.Off {
		return "off"
	}
	return b.Raw
}

// Default returns the settings used when there's no jvq.toml.
func Default() Config {
	return Config{
		Adapter:    "go",
		Rounds:     10,
		MaxRounds:  30,
		Warmup:     true,
		Confidence: 0.95,
		Timeout:    10 * time.Minute,
		Budget: Budgets{
			Time:   Budget{Pct: true, Val: 0.05, Raw: "5%"},
			Bytes:  Budget{Off: true, Raw: "off"},
			Allocs: Budget{Off: true, Raw: "off"},
			Size:   Budget{Pct: true, Val: 0.01, Raw: "1%"},
		},
	}
}

// ParseBudget reads "5%", "0", "64", "2.5" or "off". Time budgets must be
// percentages, because an absolute time budget would mean something
// different on every machine.
func ParseBudget(metric, s string) (Budget, error) {
	s = strings.TrimSpace(s)
	if s == "off" {
		return Budget{Off: true, Raw: "off"}, nil
	}
	if strings.HasSuffix(s, "%") {
		v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "%")), 64)
		if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return Budget{}, fmt.Errorf("%s budget %q: want a percentage such as \"5%%\"", metric, s)
		}
		return Budget{Pct: true, Val: v / 100, Raw: s}, nil
	}
	if metric == adapter.Time {
		return Budget{}, fmt.Errorf("time budget %q: must be a percentage such as \"5%%\"", s)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return Budget{}, fmt.Errorf("%s budget %q: want an amount such as \"0\", a percentage, or \"off\"", metric, s)
	}
	return Budget{Val: v, Raw: s}, nil
}

// Find looks for jvq.toml in dir and then in each parent up to and including
// stop (the repository root). It returns "" when there is none. Symlinks are
// resolved first, so a folder reached through one (such as /var on macOS,
// which is /private/var) still finds the file.
func Find(dir, stop string) string {
	orig := filepath.Clean(dir)
	rdir, rstop := resolve(dir), resolve(stop)
	rel, err := filepath.Rel(rstop, rdir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// dir isn't inside stop: look in dir only.
		if p := filepath.Join(orig, FileName); isFile(p) {
			return p
		}
		return ""
	}
	// Walk up in resolved paths, but return the file under the folder as
	// the caller named it, so paths in messages look like the user's own.
	up := 0
	for {
		if isFile(filepath.Join(rdir, FileName)) {
			p := orig
			for i := 0; i < up; i++ {
				p = filepath.Dir(p)
			}
			return filepath.Join(p, FileName)
		}
		if rdir == rstop {
			return ""
		}
		parent := filepath.Dir(rdir)
		if parent == rdir {
			return ""
		}
		rdir = parent
		up++
	}
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(p)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Load reads a config file on top of the defaults.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, fmt.Errorf("config file %s doesn't exist", path)
		}
		return Config{}, err
	}
	c, err := Parse(string(b))
	if err != nil {
		return Config{}, fmt.Errorf("%s: %v", path, err)
	}
	c.Source = path
	return c, nil
}

// Parse reads jvq.toml text on top of the defaults. Unknown keys are errors,
// so a typo doesn't silently fall back to a default.
func Parse(src string) (Config, error) {
	c := Default()
	m, err := parseTOML(src)
	if err != nil {
		return c, err
	}
	for _, k := range sortedKeys(m) {
		v := m[k]
		switch k {
		case "command":
			c.Command, err = str(k, v)
		case "adapter":
			c.Adapter, err = str(k, v)
			if err == nil && !adapter.Known(c.Adapter) {
				err = fmt.Errorf("adapter %q: jvq has %s", c.Adapter, strings.Join(adapter.Names, ", "))
			}
		case "build":
			c.Build, err = str(k, v)
		case "output":
			c.Output, err = str(k, v)
			if err == nil && strings.TrimSpace(c.Output) == "" {
				err = errors.New("output is empty; leave it out to read the command's standard output")
			}
		case "rounds":
			var n int64
			n, err = integer(k, v)
			if err == nil && (n < 3 || n > 1000) {
				err = fmt.Errorf("rounds = %d: use 3 to 1000", n)
			}
			c.Rounds = int(n)
		case "max_rounds":
			var n int64
			n, err = integer(k, v)
			if err == nil && (n < 3 || n > 1000) {
				err = fmt.Errorf("max_rounds = %d: use 3 to 1000", n)
			}
			c.MaxRounds = int(n)
		case "warmup":
			c.Warmup, err = boolean(k, v)
		case "fail_on_inconclusive":
			c.FailOnInconclusive, err = boolean(k, v)
		case "confidence":
			var f float64
			f, err = number(k, v)
			if err == nil && !(f >= 0.5 && f < 1) {
				err = fmt.Errorf("confidence = %v: use a level from 0.5 to below 1, such as 0.95", f)
			}
			c.Confidence = f
		case "timeout":
			var s string
			if s, err = str(k, v); err == nil {
				c.Timeout, err = time.ParseDuration(s)
				if err == nil && c.Timeout <= 0 {
					err = fmt.Errorf("timeout %q must be above zero", s)
				} else if err != nil {
					err = fmt.Errorf("timeout %q: want a duration such as \"10m\"", s)
				}
			}
		case "size":
			c.Size, err = strList(k, v)
		case "budget":
			err = c.readBudgets(v)
		case "ignore":
			err = c.readIgnore(v)
		default:
			err = fmt.Errorf("unknown setting %q", k)
		}
		if err != nil {
			return c, err
		}
	}
	return c, c.Check()
}

// Check reports settings that can't work together.
func (c Config) Check() error {
	if !adapter.Known(c.Adapter) {
		return fmt.Errorf("adapter %q: jvq has %s", c.Adapter, strings.Join(adapter.Names, ", "))
	}
	if c.Output != "" && adapter.Timed(c.Adapter) {
		return errors.New("output is set (in jvq.toml or with -output), but the time adapter reads no results; it times the command")
	}
	return nil
}

func (c *Config) readBudgets(v any) error {
	t, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("budget must be a table: [budget]")
	}
	for _, k := range sortedKeys(t) {
		val := t[k]
		if k == "bench" {
			benches, ok := val.(map[string]any)
			if !ok {
				return fmt.Errorf("budget.bench must hold tables such as [budget.bench.\"BenchmarkParse*\"]")
			}
			for _, pat := range fileOrder(benches) {
				bt, ok := benches[pat].(map[string]any)
				if !ok {
					return fmt.Errorf("budget.bench.%q must be a table", pat)
				}
				bb := BenchBudgets{Pattern: pat}
				for _, mk := range sortedKeys(bt) {
					b, err := budgetValue(mk, bt[mk])
					if err != nil {
						return fmt.Errorf("[budget.bench.%q] %v", pat, err)
					}
					switch mk {
					case adapter.Time:
						bb.Time = &b
					case adapter.Bytes:
						bb.Bytes = &b
					case adapter.Allocs:
						bb.Allocs = &b
					case adapter.Size:
						bb.Size = &b
					}
				}
				c.Bench = append(c.Bench, bb)
			}
			continue
		}
		b, err := budgetValue(k, val)
		if err != nil {
			return fmt.Errorf("[budget] %v", err)
		}
		switch k {
		case adapter.Time:
			c.Budget.Time = b
		case adapter.Bytes:
			c.Budget.Bytes = b
		case adapter.Allocs:
			c.Budget.Allocs = b
		case adapter.Size:
			c.Budget.Size = b
		}
	}
	return nil
}

func budgetValue(metric string, v any) (Budget, error) {
	switch metric {
	case adapter.Time, adapter.Bytes, adapter.Allocs, adapter.Size:
	default:
		return Budget{}, fmt.Errorf("unknown budget %q (jvq gates time, bytes, allocs and size)", metric)
	}
	switch x := v.(type) {
	case string:
		return ParseBudget(metric, x)
	case int64:
		return ParseBudget(metric, strconv.FormatInt(x, 10))
	case float64:
		return ParseBudget(metric, strconv.FormatFloat(x, 'f', -1, 64))
	}
	return Budget{}, fmt.Errorf("%s budget must be a string such as \"5%%\"", metric)
}

func (c *Config) readIgnore(v any) error {
	t, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("ignore must be a table: [ignore]")
	}
	for _, k := range sortedKeys(t) {
		if k != "bench" {
			return fmt.Errorf("unknown setting ignore.%s", k)
		}
		arr, ok := t[k].([]any)
		if !ok {
			return fmt.Errorf("ignore.bench must be a list of names such as [\"BenchmarkFlaky*\"]")
		}
		for _, x := range arr {
			s, ok := x.(string)
			if !ok {
				return fmt.Errorf("ignore.bench entries must be strings")
			}
			c.Ignore = append(c.Ignore, s)
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		if k != orderKey {
			ks = append(ks, k)
		}
	}
	sort.Strings(ks)
	return ks
}

// fileOrder lists a table's entries in the order the file has them.
func fileOrder(m map[string]any) []string {
	if o, ok := m[orderKey].([]string); ok {
		return o
	}
	return sortedKeys(m)
}

func str(k string, v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", k)
	}
	return s, nil
}

func integer(k string, v any) (int64, error) {
	n, ok := v.(int64)
	if !ok {
		return 0, fmt.Errorf("%s must be a whole number", k)
	}
	return n, nil
}

func number(k string, v any) (float64, error) {
	switch x := v.(type) {
	case int64:
		return float64(x), nil
	case float64:
		return x, nil
	}
	return 0, fmt.Errorf("%s must be a number", k)
}

func boolean(k string, v any) (bool, error) {
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be true or false", k)
	}
	return b, nil
}

// Ignored reports whether a benchmark is left out by [ignore].
func (c Config) Ignored(name, key string) bool {
	for _, p := range c.Ignore {
		if Match(p, name) || Match(p, key) {
			return true
		}
	}
	return false
}

// BudgetFor returns the budget for one metric of one benchmark: the last
// matching [budget.bench] table that sets it, or the default.
func (c Config) BudgetFor(metric, name, key string) Budget {
	b, ok := Budget{}, false
	switch metric {
	case adapter.Time:
		b, ok = c.Budget.Time, true
	case adapter.Bytes:
		b, ok = c.Budget.Bytes, true
	case adapter.Allocs:
		b, ok = c.Budget.Allocs, true
	case adapter.Size:
		b, ok = c.Budget.Size, true
	}
	if !ok {
		return Budget{Off: true, Raw: "off"}
	}
	for _, bb := range c.Bench {
		if !Match(bb.Pattern, name) && !Match(bb.Pattern, key) {
			continue
		}
		var o *Budget
		switch metric {
		case adapter.Time:
			o = bb.Time
		case adapter.Bytes:
			o = bb.Bytes
		case adapter.Allocs:
			o = bb.Allocs
		case adapter.Size:
			o = bb.Size
		}
		if o != nil {
			b = *o
		}
	}
	return b
}

// Match reports whether name matches pattern, where * matches any run of
// characters (slashes included, so sub-benchmarks match) and ? any one.
func Match(pattern, name string) bool {
	p, n := []rune(pattern), []rune(name)
	// Iterative wildcard match with backtracking to the last star.
	pi, ni, star, mark := 0, 0, -1, 0
	for ni < len(n) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == n[ni]):
			pi++
			ni++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ni
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			ni = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

func strList(k string, v any) ([]string, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list of strings such as [\"bin/app\"]", k)
	}
	var out []string
	for _, x := range arr {
		s, ok := x.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("%s entries must be file names or patterns, as strings", k)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s is empty; leave it out to gate no file sizes", k)
	}
	return out, nil
}
