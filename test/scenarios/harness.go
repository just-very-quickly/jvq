// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/just-very-quickly/jvq/internal/fixture"
)

// The harness scenario runs the real benchmark harnesses jvq has adapters
// for, end to end through jvq run: pytest-benchmark, Google Benchmark (its
// Python bindings, which write the C++ library's JSON), vitest bench and
// Rust's criterion. Each repository has a loop whose length is set per
// commit, as in the other scenarios. A harness whose tools aren't installed
// is skipped and listed as skipped.

type harness struct {
	name   string // the adapter
	bench  string // the benchmark's name in the report; "" for "work"
	needs  []string
	files  func(work int) map[string]string
	config string // jvq.toml, with %s for the shared folder
	work   int
	setup  func(shared string) error // once, before the repositories
}

const pyLoop = `def op(n):
    x = 88172645463325252
    m = (1 << 64) - 1
    for _ in range(n):
        x ^= (x << 13) & m
        x ^= x >> 7
        x ^= (x << 17) & m
    return x
`

func harnesses() []*harness {
	return []*harness{
		{
			name: "pytest-benchmark", bench: "test_work", needs: []string{python()}, work: 20000,
			files: func(w int) map[string]string {
				return map[string]string{"test_work.py": fmt.Sprintf("WORK = %d\n\n\n%s\n\ndef test_work(benchmark):\n    benchmark(op, WORK)\n", w, pyLoop)}
			},
			config: "adapter = \"pytest-benchmark\"\noutput = \"bench.json\"\n" +
				"command = \"" + python() + " -m pytest -q -p no:cacheprovider --benchmark-json=bench.json --benchmark-min-rounds=5 --benchmark-max-time=0.2 test_work.py\"\n",
			setup: func(string) error { return pyImport("pytest_benchmark") },
		},
		{
			name: "google-benchmark", needs: []string{python()}, work: 20000,
			files: func(w int) map[string]string {
				return map[string]string{"bench.py": fmt.Sprintf("import google_benchmark as benchmark\n\nWORK = %d\n\n\n%s\n\n@benchmark.register\ndef work(state):\n    while state:\n        op(WORK)\n\n\nif __name__ == \"__main__\":\n    benchmark.main()\n", w, pyLoop)}
			},
			config: "adapter = \"google-benchmark\"\ncommand = \"" + python() + " bench.py --benchmark_format=json --benchmark_min_time=0.1s\"\n",
			setup:  func(string) error { return pyImport("google_benchmark") },
		},
		{
			name: "vitest", needs: []string{"node", "npm"}, work: 200000,
			files: func(w int) map[string]string {
				return map[string]string{"work.bench.js": fmt.Sprintf(`import { bench } from 'vitest'

const WORK = %d

function op(n) {
  let x = 88172645463325252n & 0xffffffffn
  for (let i = 0; i < n; i++) {
    x ^= (x << 13n) & 0xffffffffn
    x ^= x >> 7n
    x ^= (x << 17n) & 0xffffffffn
  }
  return x
}

bench('work', () => { op(WORK) }, { time: 200 })
`, w/100)}
			},
			// The worktrees have no node_modules; the build links the
			// shared one, installed once.
			config: "adapter = \"vitest\"\noutput = \"bench.json\"\nbuild = \"ln -sfn %s/node_modules node_modules\"\n" +
				"command = \"node node_modules/vitest/vitest.mjs bench --run --outputJson bench.json\"\n",
			setup: func(shared string) error {
				os.WriteFile(filepath.Join(shared, "package.json"), []byte(`{"name": "shared", "private": true, "type": "module"}`), 0o644)
				cmd := exec.Command("npm", "install", "--no-audit", "--no-fund", "--silent", "vitest@3")
				cmd.Dir = shared
				out, err := cmd.CombinedOutput()
				if err != nil {
					return fmt.Errorf("npm install vitest: %v\n%s", err, out)
				}
				return nil
			},
		},
		{
			name: "criterion", needs: []string{"cargo"}, work: 20000,
			files: func(w int) map[string]string {
				return map[string]string{
					"Cargo.toml": "[package]\nname = \"work\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dev-dependencies]\ncriterion = { version = \"0.5\", default-features = false }\n\n[[bench]]\nname = \"work\"\nharness = false\n",
					"src/lib.rs": "",
					"benches/work.rs": fmt.Sprintf(`use criterion::{black_box, criterion_group, criterion_main, Criterion};

const WORK: u64 = %d;

fn op(n: u64) -> u64 {
    let mut x: u64 = 88172645463325252;
    for _ in 0..n {
        x ^= x << 13;
        x ^= x >> 7;
        x ^= x << 17;
    }
    x
}

fn bench(c: &mut Criterion) {
    c.bench_function("work", |b| b.iter(|| op(black_box(WORK))));
}

criterion_group! {
    name = group;
    config = Criterion::default().warm_up_time(std::time::Duration::from_millis(200)).measurement_time(std::time::Duration::from_millis(500));
    targets = bench
}
criterion_main!(group);
`, w)}
			},
			// Each side keeps its own build folder outside the worktree, so
			// later runs build incrementally.
			config: "adapter = \"criterion\"\nbuild = \"cargo bench --quiet --no-run --target-dir %[1]s/target-$JVQ_SIDE\"\n" +
				"command = \"cargo bench --quiet --target-dir %[1]s/target-$JVQ_SIDE\"\n",
		},
	}
}

func pyImport(module string) error {
	if err := exec.Command(python(), "-c", "import "+module).Run(); err != nil {
		return fmt.Errorf("python can't import %s (pip install %s)", module, strings.ReplaceAll(module, "_", "-"))
	}
	return nil
}

func harnessRuns(bin string, aa, ab int) ([]trial, []string) {
	var trials []trial
	var skipped []string
	shared, err := os.MkdirTemp("", "jvq-scenarios-harness-")
	check(err)
	defer os.RemoveAll(shared)
	for _, h := range harnesses() {
		why := ""
		for _, n := range h.needs {
			if _, err := exec.LookPath(n); err != nil {
				why = n + " isn't installed"
			}
		}
		if why == "" && h.setup != nil {
			if err := h.setup(shared); err != nil {
				why = err.Error()
			}
		}
		if why != "" {
			skipped = append(skipped, h.name+": "+why)
			fmt.Fprintf(os.Stderr, "harness %s skipped: %s\n", h.name, why)
			continue
		}
		scenario := "harness, " + h.name
		bench := h.bench
		if bench == "" {
			bench = "work"
		}
		for _, c := range []struct {
			label string
			head  int
			runs  int
		}{{"no change", h.work, aa}, {"+20% work", h.work * 12 / 10, ab}} {
			dir := harnessRepo(h, shared, c.head)
			for i := 0; i < c.runs; i++ {
				t := cli(bin, dir, scenario, c.label, bench)
				trials = append(trials, t)
				progress(t, i+1, c.runs)
			}
			os.RemoveAll(dir)
		}
	}
	return trials, skipped
}

func harnessRepo(h *harness, shared string, headWork int) string {
	dir, err := os.MkdirTemp("", "jvq-scenarios-"+h.name+"-")
	check(err)
	if _, err := fixture.Git(dir, "init", "-q", "-b", "main"); err != nil {
		fail("%v", err)
	}
	for i, w := range []int{h.work, headWork} {
		for name, body := range h.files(w) {
			p := filepath.Join(dir, filepath.FromSlash(name))
			check(os.MkdirAll(filepath.Dir(p), 0o755))
			check(os.WriteFile(p, []byte(body), 0o644))
		}
		check(os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules\nbench.json\ntarget\n"), 0o644))
		if _, err := fixture.Git(dir, "add", "-A"); err != nil {
			fail("%v", err)
		}
		if _, err := fixture.Git(dir, "commit", "-q", "--allow-empty", "-m", fmt.Sprint("commit ", i)); err != nil {
			fail("%v", err)
		}
	}
	cfg := h.config
	if strings.Contains(cfg, "%") {
		cfg = fmt.Sprintf(cfg, filepath.ToSlash(shared))
	}
	check(os.WriteFile(filepath.Join(dir, "jvq.toml"), []byte(cfg), 0o644))
	return dir
}
