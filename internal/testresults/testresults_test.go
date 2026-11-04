// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package testresults

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func attempts(t *testing.T, path string) (string, map[string]string) {
	t.Helper()
	format, rs, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, r := range rs {
		m[r.ID] += string(r.Outcome)
	}
	return format, m
}

// Real go test -json output (testdata/go-mixed.json, go test -json
// -count=2 ./... with FLAKY=1): passing, failing, skipped and flaky tests,
// subtests, a parallel test, a package that doesn't build and one whose
// test panics.
func TestGoJSONReal(t *testing.T) {
	format, got := attempts(t, "testdata/go-mixed.json")
	if format != GoJSON {
		t.Fatalf("format %q", format)
	}
	want := map[string]string{
		"example.com/cap/alpha.TestPass":         "pp",
		"example.com/cap/alpha (package)":        "s",
		"example.com/cap/crash (package)":        "s",
		"example.com/cap/alpha.TestFail":         "ff",
		"example.com/cap/alpha.TestSkip":         "ss",
		"example.com/cap/alpha.TestSub":          "ff",
		"example.com/cap/alpha.TestSub/ok":       "pp",
		"example.com/cap/alpha.TestSub/bad_case": "ff",
		"example.com/cap/alpha.TestFlaky":        "fp",
		"example.com/cap/beta.TestOne":           "pp",
		"example.com/cap/beta.TestTwo":           "pp",
		"example.com/cap/beta (package)":         "p",
		"example.com/cap/broken (build)":         "f",
		"example.com/cap/crash.TestBefore":       "p",
		"example.com/cap/crash.TestPanics":       "f",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

// A test still running when go test's -timeout killed the package
// (testdata/go-timeout.json, real) failed; the one that finished passed.
func TestGoJSONTimeout(t *testing.T) {
	_, got := attempts(t, "testdata/go-timeout.json")
	want := map[string]string{
		"example.com/hang/hang.TestQuick": "p",
		"example.com/hang/hang (package)": "s",
		"example.com/hang/hang.TestHangs": "f",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Real pytest --junitxml output: a failure, an error in a fixture, a
// skip, a class and parameters.
func TestJUnitPytest(t *testing.T) {
	format, got := attempts(t, "testdata/pytest.xml")
	if format != JUnit {
		t.Fatalf("format %q", format)
	}
	want := map[string]string{
		"test_sample.test_pass":             "p",
		"test_sample.test_fail":             "f",
		"test_sample.test_error_in_fixture": "f",
		"test_sample.test_skip":             "s",
		"test_sample.TestGroup.test_method": "p",
		"test_sample.test_param[1]":         "p",
		"test_sample.test_param[2]":         "f",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

// Surefire's rerun elements: flaky ones are failed attempts before a pass,
// rerun ones failed attempts after the first failure.
func TestJUnitSurefireReruns(t *testing.T) {
	_, got := attempts(t, "testdata/surefire-reruns.xml")
	want := map[string]string{
		"demo.DemoTest.passes":                "p",
		"demo.DemoTest.alwaysFails":           "fff",
		"demo.DemoTest.failsOnceThenPasses":   "fp",
		"demo.DemoTest.errorsTwiceThenPasses": "ffp",
		"demo.DemoTest.skipped":               "s",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestJUnitShapes(t *testing.T) {
	cases := []struct {
		name, xml string
		want      map[string]string
	}{
		{"nested suites, no classname, latin-1",
			`<?xml version="1.0" encoding="ISO-8859-1"?>
<testsuites><testsuite name="outer"><testsuite name="inner">
<testcase name="a"/><testcase name="b"><error/></testcase>
</testsuite><testcase name="c"><system-out><failure>not a failure, just text in system-out</failure></system-out></testcase></testsuite></testsuites>`,
			map[string]string{"inner.a": "p", "inner.b": "f", "outer.c": "p"}},
		{"the same case twice is two attempts",
			`<testsuite name="s"><testcase classname="k" name="t"><failure/></testcase><testcase classname="k" name="t"/></testsuite>`,
			map[string]string{"k.t": "fp"}},
		{"failure wins over skipped",
			`<testsuite><testcase classname="k" name="t"><skipped/><failure/></testcase></testsuite>`,
			map[string]string{"k.t": "f"}},
	}
	for _, c := range cases {
		format, rs, err := Parse([]byte(c.xml))
		if err != nil || format != JUnit {
			t.Errorf("%s: %v %q", c.name, err, format)
			continue
		}
		got := map[string]string{}
		for _, r := range rs {
			got[r.ID] += string(r.Outcome)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestGoJSONShapes(t *testing.T) {
	// Two go test invocations of one package (a rerun of the failed test,
	// as gotestsum --rerun-fails does), with stderr lines mixed in.
	src := `# some build output on stderr
{"Action":"start","Package":"p"}
{"Action":"run","Package":"p","Test":"TestA"}
{"Action":"fail","Package":"p","Test":"TestA"}
{"Action":"run","Package":"p","Test":"TestB"}
{"Action":"pass","Package":"p","Test":"TestB"}
{"Action":"fail","Package":"p"}
FAIL p 0.01s
{"Action":"start","Package":"p"}
{"Action":"run","Package":"p","Test":"TestA"}
{"Action":"pass","Package":"p","Test":"TestA"}
{"Action":"pass","Package":"p"}
{"Action":"fail","Package":"q","Elapsed":0}
{"Action":"skip","Package":"r"}
`
	format, rs, err := Parse([]byte(src))
	if err != nil || format != GoJSON {
		t.Fatal(err, format)
	}
	got := map[string]string{}
	for _, r := range rs {
		got[r.ID] += string(r.Outcome)
	}
	want := map[string]string{"p.TestA": "fp", "p.TestB": "p", "p (package)": "sp", "q (package)": "f"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"":                               "empty",
		"hello\nworld\n":                 "not JUnit XML or go test -json",
		"<html><body/></html>":           "not <testsuites>",
		"<testsuite><testcase name='a'>": "JUnit XML",
		"\xff\xfe<\x00":                  "UTF-16",
		`{"Action":"run","Package":"p"}` + "\n" + `{"Action":`:   "line 2",
		`<?xml version="1.0" encoding="Shift_JIS"?><testsuite/>`: "Shift_JIS",
	}
	for in, want := range cases {
		_, _, err := Parse([]byte(in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want an error with %q", in, err, want)
		}
	}
}

// One run from several files: JUnit and go test -json together, and a
// folder of reports.
func TestCollectMixed(t *testing.T) {
	dir := t.TempDir()
	reports := filepath.Join(dir, "reports")
	os.Mkdir(reports, 0o755)
	os.WriteFile(filepath.Join(reports, "a.xml"), []byte(`<testsuite><testcase classname="j" name="t1"><failure/></testcase></testsuite>`), 0o644)
	os.WriteFile(filepath.Join(reports, "b.xml"), []byte(`<testsuite><testcase classname="j" name="t1"/></testsuite>`), 0o644)
	os.WriteFile(filepath.Join(reports, "notes.txt"), []byte("ignored"), 0o644)
	os.WriteFile(filepath.Join(reports, "flaky.json"), []byte(`{"jvq": "0.7.0", "mode": "flaky"}`), 0o644)
	gj := filepath.Join(dir, "go.json")
	os.WriteFile(gj, []byte(`{"Action":"run","Package":"g","Test":"T"}`+"\n"+`{"Action":"pass","Package":"g","Test":"T"}`+"\n"), 0o644)
	run, err := Collect([]string{reports, gj})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"j.t1": "fp", "g.T": "p"}
	if !reflect.DeepEqual(run.Attempts, want) {
		t.Errorf("got %v, want %v", run.Attempts, want)
	}
	if len(run.Files) != 3 || run.Files[2].Format != GoJSON || run.Files[0].Format != JUnit {
		t.Errorf("files %+v", run.Files)
	}
	if len(run.Skipped) != 1 || filepath.Base(run.Skipped[0].Path) != "flaky.json" {
		t.Errorf("skipped %+v", run.Skipped)
	}
	// Named on its own, a file that isn't results is an error.
	if _, err := Collect([]string{filepath.Join(reports, "flaky.json")}); !errors.Is(err, ErrNotResults) {
		t.Errorf("named file: %v", err)
	}
	if _, err := Collect([]string{filepath.Join(dir, "missing.xml")}); err == nil {
		t.Error("a missing file should be an error")
	}
	empty := filepath.Join(dir, "empty")
	os.Mkdir(empty, 0o755)
	if _, err := Collect([]string{empty}); err == nil || !strings.Contains(err.Error(), "no .xml or .json") {
		t.Errorf("empty folder: %v", err)
	}
}

// Independent review, October 2026: a package whose build failed is
// "pkg (build)", told apart from a package that failed at run time with no
// failing test ("pkg (package)"), both from Go 1.24's FailedBuild field and
// from older Go's "[build failed]" / "[setup failed]" output line.
func TestGoJSONBuildFailure(t *testing.T) {
	src := `{"ImportPath":"ex.com/m/pb [ex.com/m/pb.test]","Action":"build-fail"}
{"Action":"start","Package":"ex.com/m/pb"}
{"Action":"output","Package":"ex.com/m/pb","Output":"FAIL\tex.com/m/pb [build failed]\n"}
{"Action":"fail","Package":"ex.com/m/pb","Elapsed":0,"FailedBuild":"ex.com/m/pb [ex.com/m/pb.test]"}
{"Action":"output","Package":"old/q","Output":"FAIL\told/q [build failed]\n"}
{"Action":"fail","Package":"old/q","Elapsed":0}
{"Action":"output","Package":"old/s","Output":"FAIL\told/s [setup failed]\n"}
{"Action":"fail","Package":"old/s","Elapsed":0}
{"Action":"start","Package":"ex.com/m/pc"}
{"Action":"output","Package":"ex.com/m/pc","Output":"goleak: found unexpected goroutines\n"}
{"Action":"fail","Package":"ex.com/m/pc","Elapsed":0.1}
`
	_, rs, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rs {
		got[r.ID] += string(r.Outcome)
	}
	want := map[string]string{"ex.com/m/pb (build)": "f", "old/q (build)": "f", "old/s (build)": "f", "ex.com/m/pc (package)": "f"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Parallel tests paused when a sibling panicked never ran: they aren't
// failures (real go test -json output, testdata/go-parallel-crash.json).
func TestGoJSONPausedParallelNotFailed(t *testing.T) {
	run, err := Collect([]string{filepath.Join("testdata", "go-parallel-crash.json")})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"example.com/par.TestP1", "example.com/par.TestP2"} {
		if a, ok := run.Attempts[id]; ok {
			t.Errorf("%s paused and never continued, yet has attempts %q", id, a)
		}
	}
	if run.Attempts["example.com/par.TestCrash"] != "f" {
		t.Errorf("the crash: %q", run.Attempts["example.com/par.TestCrash"])
	}
}

// Second review of 0.8 (real go test -json, Go 1.26): when one parallel
// test panics, the siblings still running are interrupted, not failed; when
// the package times out, only the tests Go names as running are blamed.
func TestGoJSONInterruptedSiblings(t *testing.T) {
	run, err := Collect([]string{filepath.Join("testdata", "go-parallel-panic-running.json")})
	if err != nil {
		t.Fatal(err)
	}
	if a := run.Attempts["example.com/a.TestFlake"]; a != "f" {
		t.Errorf("the panicking test: %q", a)
	}
	for _, id := range []string{"example.com/a.TestQ1", "example.com/a.TestQ2", "example.com/a.TestQ3"} {
		if a, ok := run.Attempts[id]; ok {
			t.Errorf("%s was interrupted by a sibling's panic, yet has attempts %q", id, a)
		}
	}
	run, err = Collect([]string{filepath.Join("testdata", "go-parallel-timeout.json")})
	if err != nil {
		t.Fatal(err)
	}
	if a := run.Attempts["example.com/a.TestFlake"]; a != "f" {
		t.Errorf("the hanging test: %q", a)
	}
	for id, a := range run.Attempts {
		if strings.Contains(a, "f") && id != "example.com/a.TestFlake" && IsTest(id) {
			t.Errorf("%s blamed for a timeout Go put on TestFlake: %q", id, a)
		}
	}
}
