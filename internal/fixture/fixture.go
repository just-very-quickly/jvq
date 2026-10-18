// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package fixture makes small git repositories holding a Go benchmark whose
// cost is set by a constant, so the tests and the accuracy suite can inject
// an exact slowdown (or an extra allocation) between two commits and check
// that jvq sees it.
package fixture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Spec sets what one commit's benchmark does.
type Spec struct {
	Work   int    // rounds of xorshift per op; the base is usually 20000
	Allocs int    // heap allocations per op
	Name   string // benchmark name; "" means BenchmarkWork
	Broken bool   // the package doesn't compile
	Extra  string // a comment, to make a commit that changes nothing measured
}

const goMod = "module example.com/fixture\n\ngo 1.21\n"

func source(s Spec) string {
	name := s.Name
	if name == "" {
		name = "BenchmarkWork"
	}
	broken := ""
	if s.Broken {
		broken = "\nvar broken int = \"not an int\"\n"
	}
	return fmt.Sprintf(`package fixture

// %s

import "testing"

// Work is how many rounds of xorshift one op does.
const Work = %d

// Allocs is how many heap allocations one op makes.
const Allocs = %d

var sink uint64
var sinkB []byte
%s
func op() {
	x := uint64(88172645463325252)
	for i := 0; i < Work; i++ {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
	}
	sink = x
	for i := 0; i < Allocs; i++ {
		sinkB = make([]byte, 16)
	}
}

func %s(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		op()
	}
}
`, s.Extra, s.Work, s.Allocs, broken, name)
}

// Git runs git in dir with a clean identity and no user or system config,
// so commits work the same on every machine.
func Git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=jvq", "GIT_AUTHOR_EMAIL=jvq@example.com",
		"GIT_COMMITTER_NAME=jvq", "GIT_COMMITTER_EMAIL=jvq@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// Repo is a fixture repository.
type Repo struct {
	Dir    string
	Subdir string // where the Go module sits, relative to Dir ("" for the root)
}

// New makes a repository in dir on branch main with one commit holding the
// base benchmark. The module goes in subdir, if given.
func New(dir, subdir string, base Spec) (*Repo, error) {
	if _, err := Git(dir, "init", "-q", "-b", "main"); err != nil {
		return nil, err
	}
	r := &Repo{Dir: dir, Subdir: subdir}
	if err := os.MkdirAll(r.ModDir(), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(r.ModDir(), "go.mod"), []byte(goMod), 0o644); err != nil {
		return nil, err
	}
	if _, err := r.Commit(base, "base"); err != nil {
		return nil, err
	}
	return r, nil
}

// ModDir is the module's directory.
func (r *Repo) ModDir() string { return filepath.Join(r.Dir, r.Subdir) }

// Commit writes the benchmark for s and commits it, returning the hash.
func (r *Repo) Commit(s Spec, msg string) (string, error) {
	if err := os.WriteFile(filepath.Join(r.ModDir(), "work_test.go"), []byte(source(s)), 0o644); err != nil {
		return "", err
	}
	if _, err := Git(r.Dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := Git(r.Dir, "commit", "-q", "--allow-empty", "-m", msg); err != nil {
		return "", err
	}
	return Git(r.Dir, "rev-parse", "HEAD")
}

// WriteConfig writes jvq.toml into the module directory (uncommitted: jvq
// reads the config from where it runs, not from the commits).
func (r *Repo) WriteConfig(toml string) error {
	return os.WriteFile(filepath.Join(r.ModDir(), "jvq.toml"), []byte(toml), 0o644)
}

// Exe is the test binary's file name on this system.
func Exe() string {
	if runtime.GOOS == "windows" {
		return "bench.test.exe"
	}
	return "./bench.test"
}

// Build compiles the benchmark once per side.
func Build() string {
	name := "bench.test"
	if runtime.GOOS == "windows" {
		name = "bench.test.exe"
	}
	return "go test -c -o " + name + " ."
}

// Command runs the compiled benchmark for about benchtime per round.
// It avoids characters that need quoting in either sh or cmd.exe.
func Command(benchtime string) string {
	return Exe() + " -test.run=NONE -test.bench=. -test.benchtime=" + benchtime
}

// Config is a jvq.toml for the fixture: build once per side, then run.
func Config(rounds int, benchtime string, extra string) string {
	return fmt.Sprintf("build = %q\ncommand = %q\nrounds = %d\n%s", Build(), Command(benchtime), rounds, extra)
}
