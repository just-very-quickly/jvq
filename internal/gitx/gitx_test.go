// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", file)
	git(t, dir, "commit", "-q", "-m", file+" "+body)
	return git(t, dir, "rev-parse", "HEAD")
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	return dir
}

func noEnv(string) string { return "" }

func TestBaseIsMergeBaseOnABranch(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	c1 := commit(t, dir, "a", "1")
	git(t, dir, "checkout", "-q", "-b", "feature")
	commit(t, dir, "a", "2")
	git(t, dir, "checkout", "-q", "main")
	commit(t, dir, "b", "main moved on")
	git(t, dir, "checkout", "-q", "feature")
	r, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := r.Resolve(ctx, "HEAD")
	p, err := r.DefaultBase(ctx, head, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if p.SHA != c1 || !strings.Contains(p.Ref, "main") {
		t.Errorf("pick %+v, want the merge base %s", p, c1)
	}
}

func TestBaseOnMainIsParent(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	c1 := commit(t, dir, "a", "1")
	commit(t, dir, "a", "2")
	r, _ := Open(ctx, dir)
	head, _ := r.Resolve(ctx, "HEAD")
	p, err := r.DefaultBase(ctx, head, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if p.SHA != c1 || p.Ref != "head^" || p.Note == "" {
		t.Errorf("pick %+v, want the parent %s", p, c1)
	}
}

func TestBaseFromGitHubBaseRef(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	c1 := commit(t, dir, "a", "1")
	git(t, dir, "branch", "release")
	commit(t, dir, "a", "2")
	r, _ := Open(ctx, dir)
	head, _ := r.Resolve(ctx, "HEAD")
	env := func(k string) string {
		if k == "GITHUB_BASE_REF" {
			return "release"
		}
		return ""
	}
	p, err := r.DefaultBase(ctx, head, env)
	if err != nil {
		t.Fatal(err)
	}
	if p.SHA != c1 {
		t.Errorf("pick %+v, want %s from GITHUB_BASE_REF", p, c1)
	}
}

func TestNoBaseBranch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "trunk")
	commit(t, dir, "a", "1")
	r, _ := Open(ctx, dir)
	head, _ := r.Resolve(ctx, "HEAD")
	if _, err := r.DefaultBase(ctx, head, noEnv); err == nil || !strings.Contains(err.Error(), "--base") {
		t.Errorf("want an error suggesting --base, got %v", err)
	}
}

func TestSingleCommitOnMain(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	commit(t, dir, "a", "1")
	r, _ := Open(ctx, dir)
	head, _ := r.Resolve(ctx, "HEAD")
	if _, err := r.DefaultBase(ctx, head, noEnv); err == nil || !strings.Contains(err.Error(), "no parent") {
		t.Errorf("got %v", err)
	}
}

func TestShallowCloneExplainsItself(t *testing.T) {
	ctx := context.Background()
	src := newRepo(t)
	commit(t, src, "a", "1")
	commit(t, src, "a", "2")
	commit(t, src, "a", "3")
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(clone), "clone", "-q", "--depth", "1", "file://"+filepath.ToSlash(src), clone)
	r, err := Open(ctx, clone)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Shallow(ctx) {
		t.Fatal("clone should be shallow")
	}
	head, _ := r.Resolve(ctx, "HEAD")
	_, err = r.DefaultBase(ctx, head, noEnv)
	if err == nil || !strings.Contains(err.Error(), "fetch-depth: 0") {
		t.Errorf("want the shallow-clone hint, got %v", err)
	}
}

func TestResolveAndDirty(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	c1 := commit(t, dir, "a", "1")
	r, _ := Open(ctx, dir)
	if got, err := r.Resolve(ctx, "main"); err != nil || got != c1 {
		t.Errorf("Resolve(main) = %q, %v", got, err)
	}
	if _, err := r.Resolve(ctx, "nope"); err == nil {
		t.Error("resolved a missing ref")
	}
	if r.Dirty(ctx) {
		t.Error("clean tree reported dirty")
	}
	os.WriteFile(filepath.Join(dir, "a"), []byte("changed"), 0o644)
	if !r.Dirty(ctx) {
		t.Error("changed tree reported clean")
	}
	if _, err := Open(ctx, t.TempDir()); err == nil {
		t.Error("opened a directory that isn't a repository")
	}
	if Short("0123456789abcdef") != "0123456789" || Short("abc") != "abc" {
		t.Error("Short")
	}
}

func TestWorkspaceAddAndRemove(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	c1 := commit(t, dir, "a", "base")
	c2 := commit(t, dir, "a", "head")
	r, _ := Open(ctx, dir)
	w, err := r.NewWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	bp, err := w.Add(ctx, "base", c1)
	if err != nil {
		t.Fatal(err)
	}
	hp, err := w.Add(ctx, "head", c2)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(bp, "a")); string(b) != "base" {
		t.Errorf("base worktree has %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(hp, "a")); string(b) != "head" {
		t.Errorf("head worktree has %q", b)
	}
	trees, _ := r.Worktrees(ctx)
	if len(trees) != 3 {
		t.Errorf("worktrees %v", trees)
	}
	if err := w.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Dir); !os.IsNotExist(err) {
		t.Errorf("workspace still there: %v", err)
	}
	trees, _ = r.Worktrees(ctx)
	if len(trees) != 1 {
		t.Errorf("worktrees after remove %v", trees)
	}
}

func TestCleanStale(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	c1 := commit(t, dir, "a", "1")
	r, _ := Open(ctx, dir)

	// A workspace whose owner died: a finished child process's id.
	dead, err := r.NewWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dead.Add(ctx, "base", c1); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "--version")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dead.Dir, ownerFile), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)

	// A workspace this process still owns, which must stay.
	mine, err := r.NewWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	defer mine.Remove()

	n, err := r.CleanStale(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("removed %d stale workspaces", n)
	}
	if _, err := os.Stat(dead.Dir); !os.IsNotExist(err) {
		t.Errorf("dead workspace still there")
	}
	if _, err := os.Stat(mine.Dir); err != nil {
		t.Errorf("live workspace removed: %v", err)
	}
	trees, _ := r.Worktrees(ctx)
	if len(trees) != 1 {
		t.Errorf("stale worktree still recorded: %v", trees)
	}
}

func TestAlive(t *testing.T) {
	if !alive(os.Getpid()) {
		t.Error("this process isn't alive")
	}
	if alive(0) || alive(-1) {
		t.Error("pid 0 or -1 alive")
	}
}
