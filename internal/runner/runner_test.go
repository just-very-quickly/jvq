// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/just-very-quickly/jvq/internal/adapter"
)

// The test binary doubles as the benchmark command: with JVQ_TEST_HELPER set
// it prints benchmark lines (or misbehaves on request) instead of testing.
func TestMain(m *testing.M) {
	if mode := os.Getenv("JVQ_TEST_HELPER"); mode != "" {
		helper(mode)
		return
	}
	os.Exit(m.Run())
}

func helper(mode string) {
	side := os.Getenv("JVQ_SIDE")
	switch mode {
	case "bench":
		ns := 100
		if side == "head" {
			ns = 200
		}
		wd, _ := os.Getwd()
		fmt.Printf("pkg: example\nBenchmarkA-2 10 %d ns/op\n", ns)
		fmt.Fprintf(os.Stderr, "running in %s\n", wd)
		if f := os.Getenv("JVQ_TEST_LOG"); f != "" {
			appendLine(f, side+" "+os.Getenv("JVQ_ROUND")+" "+filepath.Base(wd))
		}
	case "fail-head":
		if side == "head" {
			fmt.Println("compile error: something broke")
			os.Exit(3)
		}
		fmt.Println("BenchmarkA-2 10 100 ns/op")
	case "nothing":
		fmt.Println("PASS")
	case "nap":
		d := 20 * time.Millisecond
		if side == "head" {
			d = 80 * time.Millisecond
		}
		time.Sleep(d)
	case "write-file":
		// Results go to a file; stdout holds only chatter.
		fmt.Println("BenchmarkStdout-2 10 999 ns/op")
		if os.Getenv("JVQ_ROUND") != "3" || side != "head" {
			os.WriteFile("results.json", []byte(`{"name": "a", "unit": "ns", "value": 7}`), 0o644)
		}
	case "sleep":
		time.Sleep(time.Minute)
	}
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	fmt.Fprintln(f, line)
	f.Close()
}

func self(t *testing.T, mode string) string {
	t.Helper()
	t.Setenv("JVQ_TEST_HELPER", mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return `"` + exe + `"`
}

func sides(t *testing.T) (Side, Side) {
	t.Helper()
	root := t.TempDir()
	b, h := filepath.Join(root, "base"), filepath.Join(root, "head")
	for _, d := range []string{b, h, filepath.Join(b, "sub"), filepath.Join(h, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return Side{Name: "base", Dir: b}, Side{Name: "head", Dir: h}
}

func TestPlanInterleaves(t *testing.T) {
	if got := Plan(3, false); !reflect.DeepEqual(got, []int{0, 1, 1, 0, 0, 1}) {
		t.Errorf("ABBA plan %v", got)
	}
	if got := Plan(2, true); !reflect.DeepEqual(got, []int{0, 0, 1, 1}) {
		t.Errorf("sequential plan %v", got)
	}
}

func TestRunCollectsBothSides(t *testing.T) {
	base, head := sides(t)
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("JVQ_TEST_LOG", log)
	cmd := self(t, "bench")
	var progress strings.Builder
	res, err := Run(context.Background(), Options{
		Command: cmd, Adapter: "go", Rounds: 4, Warmup: true, Subdir: "sub", Progress: &progress,
	}, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Base) != 4 || len(res.Head) != 4 {
		t.Fatalf("got %d base and %d head samples", len(res.Base), len(res.Head))
	}
	if res.Base[0].Value != 100 || res.Head[0].Value != 200 || res.Base[0].Package != "example" {
		t.Errorf("samples %+v / %+v", res.Base[0], res.Head[0])
	}
	want := []string{"base", "head", "head", "base", "base", "head", "head", "base"}
	if !reflect.DeepEqual(res.Order, want) {
		t.Errorf("order %v", res.Order)
	}
	if len(res.Wall["base"]) != 4 || len(res.Wall["head"]) != 4 {
		t.Errorf("wall %v", res.Wall)
	}
	b, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	// two warm-up runs (round 0) and eight measured runs, all in sub/
	if len(lines) != 10 || lines[0] != "base 0 sub" || lines[1] != "head 0 sub" || lines[2] != "base 1 sub" || lines[9] != "base 4 sub" {
		t.Errorf("runs:\n%s", b)
	}
	if !strings.Contains(progress.String(), "round 4/4") {
		t.Errorf("progress:\n%s", progress.String())
	}
}

func TestBuildRunsOncePerSide(t *testing.T) {
	base, head := sides(t)
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("JVQ_TEST_LOG", log)
	exe := self(t, "bench")
	// The helper stands in for the build command too: it logs one line per
	// side before the six measured runs. Warm-up is off here.
	_, err := Run(context.Background(), Options{Command: exe, Build: exe, Adapter: "go", Rounds: 3}, base, head)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 8 || !strings.HasPrefix(lines[0], "base 0") || !strings.HasPrefix(lines[1], "head 0") {
		t.Errorf("runs:\n%s", b)
	}
}

func TestHeadFailureNamesTheSideAndShowsOutput(t *testing.T) {
	base, head := sides(t)
	_, err := Run(context.Background(), Options{Command: self(t, "fail-head"), Adapter: "go", Rounds: 3}, base, head)
	var se *StepError
	if !errors.As(err, &se) {
		t.Fatalf("got %v", err)
	}
	if se.Side != "head" || se.Stage != "run" || !strings.Contains(se.Output, "something broke") {
		t.Errorf("side %q stage %q output %q", se.Side, se.Stage, se.Output)
	}
	if !strings.Contains(err.Error(), "head run failed") {
		t.Errorf("message %q", err)
	}
}

func TestNoResults(t *testing.T) {
	base, head := sides(t)
	_, err := Run(context.Background(), Options{Command: self(t, "nothing"), Adapter: "go", Rounds: 3, Warmup: true}, base, head)
	if !errors.Is(err, ErrNoResults) {
		t.Fatalf("got %v", err)
	}
	var se *StepError
	if errors.As(err, &se); se.Side != "base" || !strings.Contains(se.Output, "PASS") {
		t.Errorf("%+v", se)
	}
	if _, err := Run(context.Background(), Options{Command: self(t, "bench"), Adapter: "nope", Rounds: 3}, base, head); err == nil {
		t.Error("unknown adapter accepted")
	}
	if _, err := Run(context.Background(), Options{Command: "x", Rounds: 0}, base, head); err == nil {
		t.Error("zero rounds accepted")
	}
}

func TestTimeoutStopsTheCommand(t *testing.T) {
	base, head := sides(t)
	start := time.Now()
	_, err := Run(context.Background(), Options{Command: self(t, "sleep"), Adapter: "go", Rounds: 3, Timeout: 300 * time.Millisecond}, base, head)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %s to stop", d)
	}
}

func TestCancelStopsTheCommand(t *testing.T) {
	base, head := sides(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := Run(ctx, Options{Command: self(t, "sleep"), Adapter: "go", Rounds: 3}, base, head)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %s to stop", d)
	}
}

func TestAdaptiveRoundsKeepTheAlternation(t *testing.T) {
	base, head := sides(t)
	asked := 0
	res, err := Run(context.Background(), Options{
		Command: self(t, "bench"), Adapter: "go", Rounds: 3, MaxRounds: 9,
		More: func(r *Result) bool { asked++; return r.Rounds < 7 },
	}, base, head)
	if err != nil {
		t.Fatal(err)
	}
	// 3 rounds, then two more twice (to 7), then More says stop.
	if res.Rounds != 7 || len(res.Base) != 7 || len(res.Head) != 7 || asked != 3 {
		t.Fatalf("rounds %d, samples %d/%d, asked %d", res.Rounds, len(res.Base), len(res.Head), asked)
	}
	want := []string{"base", "head", "head", "base", "base", "head", // rounds 1-3
		"head", "base", "base", "head", // rounds 4-5
		"head", "base", "base", "head"} // rounds 6-7
	if !reflect.DeepEqual(res.Order, want) {
		t.Errorf("order %v", res.Order)
	}
	// The cap holds even when More always wants more.
	res, err = Run(context.Background(), Options{
		Command: self(t, "bench"), Adapter: "go", Rounds: 3, MaxRounds: 6,
		More: func(*Result) bool { return true },
	}, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rounds != 5 {
		t.Errorf("rounds %d, want 5 (adding two at a time, never past 6)", res.Rounds)
	}
	// MaxRounds at or below Rounds: no extra rounds.
	res, _ = Run(context.Background(), Options{
		Command: self(t, "bench"), Adapter: "go", Rounds: 3, MaxRounds: 3,
		More: func(*Result) bool { t.Error("More asked with no room"); return true },
	}, base, head)
	if res.Rounds != 3 {
		t.Errorf("rounds %d", res.Rounds)
	}
}

func TestBackgroundChildDoesNotFailASuccessfulRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	base, head := sides(t)
	// The command succeeds but leaves a child holding its output open.
	cmd := `echo "BenchmarkA-2 10 1 ns/op"; (sleep 30 &)`
	res, err := Run(context.Background(), Options{Command: cmd, Adapter: "go", Rounds: 1}, base, head)
	if err != nil {
		t.Fatalf("a successful run failed: %v", err)
	}
	if len(res.Base) != 1 || len(res.Head) != 1 {
		t.Errorf("samples %+v", res)
	}
}

func TestTimeAdapterTimesTheCommand(t *testing.T) {
	base, head := sides(t)
	res, err := Run(context.Background(), Options{Command: self(t, "nap"), Adapter: "time", Rounds: 3, Warmup: true}, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Base) != 3 || len(res.Head) != 3 {
		t.Fatalf("got %d and %d samples", len(res.Base), len(res.Head))
	}
	for i := range res.Base {
		b, h := res.Base[i], res.Head[i]
		if b.Name != "command" || b.Metric != "time" || b.Value < 20e6 || h.Value < 80e6 || h.Value < b.Value {
			t.Errorf("round %d: base %+v head %+v", i+1, b, h)
		}
	}
}

func TestOutputFile(t *testing.T) {
	base, head := sides(t)
	cmd := self(t, "write-file")
	// The helper skips writing on head's round 3. The file from round 2 is
	// removed before each run, so that run must fail rather than count the
	// old file again.
	_, err := Run(context.Background(), Options{Command: cmd, Adapter: "json", Output: "results.json", Rounds: 4, Subdir: "sub"}, base, head)
	var se *StepError
	if !errors.As(err, &se) || se.Side != "head" || !strings.Contains(err.Error(), "didn't write its output file") {
		t.Fatalf("got %v", err)
	}
	res, err := Run(context.Background(), Options{Command: cmd, Adapter: "json", Output: "results.json", Rounds: 2, Subdir: "sub"}, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Base) != 2 || res.Base[0].Name != "a" || res.Base[0].Value != 7 {
		t.Errorf("read stdout instead of the file? %+v", res.Base)
	}
	if _, err := os.Stat(filepath.Join(base.Dir, "sub", "results.json")); err != nil {
		t.Errorf("the output file should be where the command runs: %v", err)
	}
}

func TestNoResultsHintFollowsTheAdapter(t *testing.T) {
	base, head := sides(t)
	_, err := Run(context.Background(), Options{Command: self(t, "nothing"), Adapter: "json", Rounds: 3}, base, head)
	if !errors.Is(err, ErrNoResults) || !strings.Contains(err.Error(), "json adapter") {
		t.Errorf("got %v", err)
	}
}

func TestTimeAdapterStopsAtExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	base, head := sides(t)
	// The command exits at once but leaves a child holding its output for
	// two seconds. The clock must stop at the exit.
	res, err := Run(context.Background(), Options{Command: "sleep 2 & echo started", Adapter: "time", Rounds: 1}, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if v := res.Base[0].Value; v > 1e9 {
		t.Errorf("timed %.2fs, want the command's own few milliseconds", v/1e9)
	}
}

func TestPerRunTakesTheMedian(t *testing.T) {
	in := []adapter.Sample{
		{Name: "a", Metric: "time", Unit: "ns", Value: 3},
		{Name: "b", Metric: "time", Unit: "ns", Value: 7},
		{Name: "a", Metric: "time", Unit: "ns", Value: 1},
		{Name: "a", Metric: "time", Unit: "ns", Value: 2},
		{Name: "a", Metric: "bytes", Unit: "B", Value: 16},
		{Package: "p", Name: "a", Metric: "time", Unit: "ns", Value: 9},
	}
	got := PerRun(in)
	want := []adapter.Sample{
		{Name: "a", Metric: "time", Unit: "ns", Value: 2},
		{Name: "b", Metric: "time", Unit: "ns", Value: 7},
		{Name: "a", Metric: "bytes", Unit: "B", Value: 16},
		{Package: "p", Name: "a", Metric: "time", Unit: "ns", Value: 9},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestSumInstructions(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "vg.1"), []byte("==1== Cachegrind\n==1== I refs:        13,438,495\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "vg.2"), []byte("==2== I   refs:      1,000\n"), 0o644)
	if n, err := sumInstructions(dir); err != nil || n != 13439495 {
		t.Errorf("sum %v, %v", n, err)
	}
	empty := t.TempDir()
	os.WriteFile(filepath.Join(empty, "vg.3"), []byte("==3== valgrind: Unrecognised instruction\n"), 0o644)
	if _, err := sumInstructions(empty); err == nil {
		t.Error("a log with no count passed")
	}
	if !strings.Contains(valgrindTail(empty), "Unrecognised instruction") {
		t.Errorf("tail %q", valgrindTail(empty))
	}
	if _, err := sumInstructions(t.TempDir()); err == nil {
		t.Error("no logs passed")
	}
}

func TestShQuote(t *testing.T) {
	if got := shQuote(`echo 'a b' "c"`); got != `'echo '\''a b'\'' "c"'` {
		t.Errorf("got %s", got)
	}
}
