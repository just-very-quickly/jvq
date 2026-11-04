// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package testresults reads test results from one CI run: JUnit XML (what
// most test runners can write: Maven Surefire, pytest, gotestsum, Jest
// reporters, GitLab and Jenkins plugins) and the event stream of
// `go test -json`.
//
// Every result of a test is one attempt, in the order the file has them.
// A test run twice (go test -count=2, a rerun of failed tests, a testcase
// listed again) has two attempts, so a test that failed and then passed on
// the same code shows as exactly that.
package testresults

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Attempt outcomes, as the letters the history file keeps.
const (
	Pass = 'p'
	Fail = 'f'
	Skip = 's'
)

// Formats a file can be in.
const (
	JUnit  = "junit"
	GoJSON = "go-json"
)

// Result is one attempt of one test.
type Result struct {
	ID      string
	Outcome byte
}

// Skipped is a file in a results folder that was left out, and why.
type Skipped struct {
	Path, Reason string
}

// ErrNotResults is returned for a file in neither format.
var ErrNotResults = errors.New("not JUnit XML or go test -json output")

// File is what one results file held.
type File struct {
	Path   string
	Format string
	Tests  int // distinct tests
}

// Run is every attempt of every test in one CI run, from one or more files.
type Run struct {
	Files []File
	// Skipped lists files in a results folder that aren't test results
	// (a package.json, jvq's own JSON report), which are left out.
	Skipped []Skipped
	// Attempts maps a test to its attempts in order, such as "fp" for a
	// test that failed and then passed.
	Attempts map[string]string
}

// IDs returns the run's tests, sorted.
func (r *Run) IDs() []string {
	ids := make([]string, 0, len(r.Attempts))
	for id := range r.Attempts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Collect reads results from paths: files, or folders, whose *.xml and
// *.json files are read (not their subfolders). All of them together are
// one CI run, so shards and a mix of JUnit and go test -json add up.
func Collect(paths []string) (*Run, error) {
	run := &Run{Attempts: map[string]string{}}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		files := []string{p}
		inFolder := st.IsDir()
		if st.IsDir() {
			files = nil
			ents, err := os.ReadDir(p)
			if err != nil {
				return nil, err
			}
			for _, e := range ents {
				ext := strings.ToLower(filepath.Ext(e.Name()))
				if !e.IsDir() && (ext == ".xml" || ext == ".json") {
					files = append(files, filepath.Join(p, e.Name()))
				}
			}
			if len(files) == 0 {
				return nil, fmt.Errorf("%s: no .xml or .json files in this folder", p)
			}
		}
		for _, f := range files {
			format, results, err := ReadFile(f)
			if inFolder && errors.Is(err, ErrNotResults) {
				run.Skipped = append(run.Skipped, Skipped{Path: f, Reason: ErrNotResults.Error()})
				continue
			}
			if err != nil {
				return nil, err
			}
			seen := map[string]bool{}
			for _, r := range results {
				run.Attempts[r.ID] += string(r.Outcome)
				seen[r.ID] = true
			}
			run.Files = append(run.Files, File{Path: f, Format: format, Tests: len(seen)})
		}
	}
	return run, nil
}

// ReadFile reads one results file, telling the format from its content.
func ReadFile(path string) (format string, results []Result, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	format, results, err = Parse(b)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", path, err)
	}
	return format, results, nil
}

// Parse reads results in either format: JUnit XML when the content starts
// with <, go test -json when it has a line that is a JSON test event.
func Parse(b []byte) (string, []Result, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if len(b) >= 2 && (b[0] == 0xff && b[1] == 0xfe || b[0] == 0xfe && b[1] == 0xff) {
		return "", nil, errors.New("the file is UTF-16; write it as UTF-8")
	}
	t := bytes.TrimSpace(b)
	if len(t) == 0 {
		return "", nil, errors.New("the file is empty")
	}
	if t[0] == '<' {
		r, err := parseJUnit(b)
		return JUnit, r, err
	}
	if looksLikeGoJSON(b) {
		r, err := parseGoJSON(b)
		return GoJSON, r, err
	}
	return "", nil, ErrNotResults
}

func looksLikeGoJSON(b []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		l := bytes.TrimSpace(sc.Bytes())
		if len(l) == 0 || l[0] != '{' {
			continue
		}
		var ev goEvent
		return json.Unmarshal(l, &ev) == nil && ev.Action != ""
	}
	return false
}

// junit reads JUnit XML. A <testcase> fails if it holds <failure> or
// <error>, is skipped if it holds <skipped>, and passes otherwise.
// Surefire's reruns add attempts: each <flakyFailure> or <flakyError> is a
// failed attempt before the pass, and each <rerunFailure> or <rerunError>
// a failed attempt after the first failure. Suites may nest.
func parseJUnit(b []byte) ([]Result, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.CharsetReader = charsetReader
	var (
		out    []Result
		suites []string // enclosing testsuite names
		cur    *junitCase
		depth  int // element depth inside the current testcase
		sawTop bool
	)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("JUnit XML: %v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !sawTop {
				sawTop = true
				if t.Name.Local != "testsuites" && t.Name.Local != "testsuite" {
					return nil, fmt.Errorf("JUnit XML: starts with <%s>, not <testsuites> or <testsuite>", t.Name.Local)
				}
			}
			if cur != nil {
				depth++
				if depth == 1 {
					switch t.Name.Local {
					case "failure", "error":
						cur.failed = true
					case "skipped":
						cur.skipped = true
					case "flakyFailure", "flakyError":
						cur.flaky++
					case "rerunFailure", "rerunError":
						cur.reruns++
					}
				}
				continue
			}
			switch t.Name.Local {
			case "testsuite":
				suites = append(suites, attr(t, "name"))
			case "testcase":
				cur = &junitCase{name: attr(t, "name"), class: attr(t, "classname")}
				if cur.class == "" && len(suites) > 0 {
					cur.class = suites[len(suites)-1]
				}
				depth = 0
			}
		case xml.EndElement:
			if cur != nil {
				if depth > 0 {
					depth--
					continue
				}
				out = append(out, cur.results()...)
				cur = nil
				continue
			}
			if t.Name.Local == "testsuite" && len(suites) > 0 {
				suites = suites[:len(suites)-1]
			}
		}
	}
	if !sawTop {
		return nil, errors.New("JUnit XML: no elements")
	}
	return out, nil
}

type junitCase struct {
	name, class     string
	failed, skipped bool
	flaky, reruns   int
}

func (c *junitCase) results() []Result {
	id := c.name
	if c.class != "" {
		id = c.class + "." + c.name
	}
	var out []Result
	add := func(o byte, n int) {
		for i := 0; i < n; i++ {
			out = append(out, Result{ID: id, Outcome: o})
		}
	}
	switch {
	case c.failed:
		add(Fail, 1+c.reruns)
	case c.skipped:
		add(Skip, 1)
	default:
		add(Fail, c.flaky)
		add(Pass, 1)
	}
	return out
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// charsetReader accepts the single-byte encodings JUnit files are written
// in besides UTF-8 (which the decoder reads itself).
func charsetReader(charset string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "us-ascii", "ascii", "iso-8859-1", "latin1", "latin-1", "windows-1252", "cp1252":
		b, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}
		var sb strings.Builder
		for _, c := range b {
			sb.WriteRune(rune(c)) // Latin-1 maps byte for code point
		}
		return strings.NewReader(sb.String()), nil
	}
	return nil, fmt.Errorf("encoding %q isn't supported; write UTF-8", charset)
}

type goEvent struct {
	Action      string
	Package     string
	Test        string
	Output      string
	FailedBuild string
}

// PackageSuffix marks the entry for a Go package that failed at run time
// with no test to blame: a crash in TestMain, a leak check after the
// tests, or a timeout before any test ran.
const PackageSuffix = " (package)"

// BuildSuffix marks the entry for a Go package whose test binary didn't
// build (or whose setup failed). It only ever fails, so it can never be
// flaky and never goes into quarantine: a compile error always blocks,
// even in a package whose (package) entry is in quarantine.
const BuildSuffix = " (build)"

// IsTest reports whether id is a test, not a Go package's own entry.
func IsTest(id string) bool {
	return !strings.HasSuffix(id, PackageSuffix) && !strings.HasSuffix(id, BuildSuffix)
}

// parseGoJSON reads go test -json events. Each pass, fail or skip of a
// test is an attempt. When a package fails, every test of it that started
// and didn't finish (a panic or a timeout) gets a failed attempt; if no
// test of it failed at all, the package itself does, as "pkg (package)",
// so a crash outside a test blocks like a failed test; when a test is to
// blame, the package's entry is a skip, there only to say which tests
// are the package's. A package that
// passes gets a passing "pkg (package)" attempt, so that entry can
// recover. A package whose build or setup failed (Go 1.24's FailedBuild,
// or the "[build failed]" line older Go prints) fails as "pkg (build)"
// instead, which never passes. Lines that aren't JSON (stderr mixed in)
// are skipped.
func parseGoJSON(b []byte) ([]Result, error) {
	type pkgState struct {
		pending     map[string]int
		paused      map[string]bool // t.Parallel tests waiting their turn
		// Go's "panic: test timed out" names the tests still running
		// under "running tests:"; those are the ones to blame.
		timedOut   map[string]bool
		inTimedOut bool
		order       []string
		testFail    bool
		buildFailed bool
	}
	pkgs := map[string]*pkgState{}
	get := func(p string) *pkgState {
		s := pkgs[p]
		if s == nil {
			s = &pkgState{pending: map[string]int{}, paused: map[string]bool{}}
			pkgs[p] = s
		}
		return s
	}
	var out []Result
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		l := bytes.TrimSpace(sc.Bytes())
		if len(l) == 0 || l[0] != '{' {
			continue
		}
		var ev goEvent
		if err := json.Unmarshal(l, &ev); err != nil {
			return nil, fmt.Errorf("go test -json, line %d: %v", line, err)
		}
		if ev.Package == "" {
			continue // build-output and build-fail lines; the package's fail event follows
		}
		id := ev.Package + "." + ev.Test
		switch ev.Action {
		case "output":
			if s := pkgs[ev.Package]; s != nil || strings.Contains(ev.Output, "running tests:") {
				s := get(ev.Package)
				switch out := strings.TrimRight(ev.Output, "\r\n"); {
				case strings.TrimSpace(out) == "running tests:":
					s.inTimedOut = true
				case s.inTimedOut && strings.HasPrefix(out, "\t"):
					if f := strings.Fields(out); len(f) > 0 {
						if s.timedOut == nil {
							s.timedOut = map[string]bool{}
						}
						s.timedOut[f[0]] = true
					}
				default:
					s.inTimedOut = false
				}
			}
			if ev.Test == "" && strings.HasPrefix(ev.Output, "FAIL") &&
				(strings.Contains(ev.Output, " [build failed]") || strings.Contains(ev.Output, " [setup failed]")) {
				get(ev.Package).buildFailed = true
			}
		case "start":
			delete(pkgs, ev.Package) // a new go test of this package
		case "pause":
			if ev.Test != "" {
				get(ev.Package).paused[ev.Test] = true
			}
		case "cont":
			if ev.Test != "" {
				delete(get(ev.Package).paused, ev.Test)
			}
		case "run":
			if ev.Test != "" {
				s := get(ev.Package)
				if s.pending[ev.Test] == 0 {
					s.order = append(s.order, ev.Test)
				}
				s.pending[ev.Test]++
			}
		case "pass", "fail", "skip":
			if ev.Test != "" {
				s := get(ev.Package)
				if s.pending[ev.Test] > 0 {
					s.pending[ev.Test]--
				}
				o := byte(Pass)
				if ev.Action == "fail" {
					o = Fail
					s.testFail = true
				} else if ev.Action == "skip" {
					o = Skip
				}
				out = append(out, Result{ID: id, Outcome: o})
				continue
			}
			// The package's own end.
			s := get(ev.Package)
			switch ev.Action {
			case "fail":
				if ev.FailedBuild != "" || s.buildFailed {
					out = append(out, Result{ID: ev.Package + BuildSuffix, Outcome: Fail})
					break
				}
				hung := false
				for _, t := range s.order {
					if s.paused[t] {
						// A t.Parallel test still waiting for its turn when
						// the package stopped: it never ran, so no attempt.
						continue
					}
					switch {
					case len(s.timedOut) > 0 && !s.timedOut[t]:
						// A timeout: Go named the tests still running, and
						// this isn't one of them.
						continue
					case len(s.timedOut) == 0 && s.testFail:
						// Another test failed (a panic gets its own fail
						// event) and stopped the package: this one was
						// running beside it, interrupted, not failed.
						continue
					}
					for i := 0; i < s.pending[t]; i++ {
						out = append(out, Result{ID: ev.Package + "." + t, Outcome: Fail})
						hung = true
					}
				}
				if !s.testFail && !hung {
					out = append(out, Result{ID: ev.Package + PackageSuffix, Outcome: Fail})
				} else {
					// A test is to blame. The package's entry is still
					// there, skipped (no evidence either way), so the
					// history knows which tests are the package's.
					out = append(out, Result{ID: ev.Package + PackageSuffix, Outcome: Skip})
				}
			case "pass":
				out = append(out, Result{ID: ev.Package + PackageSuffix, Outcome: Pass})
			}
			delete(pkgs, ev.Package)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("go test -json: %v", err)
	}
	return out, nil
}
