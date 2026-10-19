// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package gitx finds the two commits jvq compares and checks each out into
// its own worktree, so base and head build and run side by side from clean
// trees without touching the user's checkout.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Repo is a git repository jvq works in.
type Repo struct {
	Root string // the top of the working tree
}

// Error is a git failure with the command and its output.
type Error struct {
	Args   []string
	Output string
	Err    error
}

func (e *Error) Error() string {
	out := strings.TrimSpace(e.Output)
	if out == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), out)
}

func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", &Error{Args: args, Output: errb.String() + out.String(), Err: err}
	}
	return strings.TrimSpace(out.String()), nil
}

// Open finds the repository that holds dir.
func Open(ctx context.Context, dir string) (*Repo, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, errors.New("git isn't installed or isn't on PATH")
	}
	root, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s isn't inside a git repository", dir)
	}
	return &Repo{Root: filepath.Clean(root)}, nil
}

// Git runs git in the repository.
func (r *Repo) Git(ctx context.Context, args ...string) (string, error) {
	return run(ctx, r.Root, args...)
}

// Resolve turns a ref into a full commit hash.
func (r *Repo) Resolve(ctx context.Context, ref string) (string, error) {
	sha, err := r.Git(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || sha == "" {
		return "", fmt.Errorf("can't find %q in this repository", ref)
	}
	return sha, nil
}

// Shallow reports whether the repository is a shallow clone.
func (r *Repo) Shallow(ctx context.Context) bool {
	out, err := r.Git(ctx, "rev-parse", "--is-shallow-repository")
	return err == nil && out == "true"
}

// Dirty reports whether the working tree has uncommitted changes to tracked
// files. jvq compares commits, so those changes aren't measured.
func (r *Repo) Dirty(ctx context.Context) bool {
	out, err := r.Git(ctx, "status", "--porcelain", "--untracked-files=no")
	return err == nil && out != ""
}

// ShallowHint is added to errors where a missing commit is likely caused by
// a shallow clone, which is the default in CI.
const ShallowHint = "this is a shallow clone, so older commits aren't here; " +
	"fetch the history (in GitHub Actions: actions/checkout with fetch-depth: 0)"

// Pick is the outcome of choosing the base commit.
type Pick struct {
	Ref  string // what was resolved, for reports
	SHA  string
	Note string // how it was chosen, when that isn't obvious
}

// DefaultBase picks the base to compare head with when none is given: the
// merge base of head and the branch it will merge into. In a GitHub pull
// request run that branch is GITHUB_BASE_REF; otherwise it is origin's
// default branch, or main or master. When head is already on that branch
// (a push to main), the base is head's parent, so each commit is checked
// against the one before it.
func (r *Repo) DefaultBase(ctx context.Context, headSHA string, env func(string) string) (Pick, error) {
	if env("CI_MERGE_REQUEST_IID") != "" {
		if p, ok, err := r.gitlabBase(ctx, headSHA, env); ok || err != nil {
			return p, err
		}
	}
	var candidates []string
	if b := env("GITHUB_BASE_REF"); b != "" {
		candidates = append(candidates, "origin/"+b, b)
	}
	if b := env("CI_MERGE_REQUEST_TARGET_BRANCH_NAME"); b != "" {
		candidates = append(candidates, "origin/"+b, b)
	}
	if b := env("CI_DEFAULT_BRANCH"); b != "" {
		candidates = append(candidates, "origin/"+b, b)
	}
	if sym, err := r.Git(ctx, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil && sym != "" {
		candidates = append(candidates, strings.TrimPrefix(sym, "refs/remotes/"))
	}
	candidates = append(candidates, "origin/main", "origin/master", "main", "master")
	for _, c := range candidates {
		tip, err := r.Resolve(ctx, c)
		if err != nil {
			continue
		}
		mb, err := r.Git(ctx, "merge-base", tip, headSHA)
		if err != nil || mb == "" {
			hint := ""
			if r.Shallow(ctx) {
				hint = "; " + ShallowHint
			}
			return Pick{}, fmt.Errorf("can't find where head and %s meet%s", c, hint)
		}
		if mb != headSHA {
			return Pick{Ref: "merge-base(" + c + ", head)", SHA: mb}, nil
		}
		parent, err := r.Resolve(ctx, headSHA+"^")
		if err != nil {
			hint := ""
			if r.Shallow(ctx) {
				hint = "; " + ShallowHint
			}
			return Pick{}, fmt.Errorf("head is on %s and has no parent commit to compare with%s", c, hint)
		}
		return Pick{Ref: "head^", SHA: parent, Note: "head is on " + c + ", so it is compared with its parent"}, nil
	}
	hint := ""
	if r.Shallow(ctx) {
		hint = "; " + ShallowHint
	}
	return Pick{}, fmt.Errorf("no base branch found (looked for %s); pass --base%s", strings.Join(candidates, ", "), hint)
}

// GitLabHint says what a GitLab job needs for jvq to find the base.
const GitLabHint = `in .gitlab-ci.yml set GIT_DEPTH: "0", and fetch the target branch first: git fetch origin "$CI_MERGE_REQUEST_TARGET_BRANCH_NAME"`

// gitlabBase picks the base in a GitLab merge request pipeline. In a
// merged results pipeline (or a merge train) head is GitLab's merge of the
// change into the target branch, so its first parent is the target as it
// stands. Otherwise GitLab names the merge request's base commit. ok is
// false when neither applies, and the caller falls back to the target
// branch by name.
func (r *Repo) gitlabBase(ctx context.Context, headSHA string, env func(string) string) (Pick, bool, error) {
	switch env("CI_MERGE_REQUEST_EVENT_TYPE") {
	case "merged_result", "merge_train":
		parent, err := r.Resolve(ctx, headSHA+"^1")
		if err != nil {
			return Pick{}, false, fmt.Errorf("head is GitLab's merged result, but its first parent isn't in this clone; %s", GitLabHint)
		}
		return Pick{Ref: "head^1", SHA: parent, Note: "head is GitLab's merged result, so it is compared with the target branch it was merged into"}, true, nil
	}
	if sha := env("CI_MERGE_REQUEST_DIFF_BASE_SHA"); sha != "" {
		got, err := r.Resolve(ctx, sha)
		if err != nil {
			return Pick{}, false, fmt.Errorf("the merge request's base commit %s isn't in this clone; %s", Short(sha), GitLabHint)
		}
		return Pick{Ref: "CI_MERGE_REQUEST_DIFF_BASE_SHA", SHA: got}, true, nil
	}
	return Pick{}, false, nil
}

// Workspace is a temporary directory holding a worktree per side.
type Workspace struct {
	Dir   string
	repo  *Repo
	trees []string
}

// prefix names jvq's temporary directories, so stale ones can be found.
const prefix = "jvq-run-"

// ownerFile records the process that owns a workspace: its id and the
// host name, so a run never judges a workspace made on another machine or
// in another container that shares the temp folder.
const ownerFile = "jvq-owner"

func ownerLine() string {
	h, _ := os.Hostname()
	return strconv.Itoa(os.Getpid()) + " " + h
}

// NewWorkspace makes a temporary directory owned by this process.
func (r *Repo) NewWorkspace() (*Workspace, error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, ownerFile), []byte(ownerLine()), 0o644); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return &Workspace{Dir: dir, repo: r}, nil
}

// Add checks out sha into a detached worktree named name in the workspace.
func (w *Workspace) Add(ctx context.Context, name, sha string) (string, error) {
	path := filepath.Join(w.Dir, name)
	if _, err := w.repo.Git(ctx, "worktree", "add", "--detach", "--force", path, sha); err != nil {
		return "", err
	}
	w.trees = append(w.trees, path)
	return path, nil
}

// Remove deletes the worktrees and the workspace. It uses a fresh context so
// it still runs after the run's context was cancelled.
func (w *Workspace) Remove() error {
	var errs []error
	for _, t := range w.trees {
		if _, err := w.repo.Git(context.Background(), "worktree", "remove", "--force", t); err != nil {
			errs = append(errs, err)
		}
	}
	if err := os.RemoveAll(w.Dir); err != nil {
		errs = append(errs, err)
	}
	if _, err := w.repo.Git(context.Background(), "worktree", "prune"); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// CleanStale removes workspaces left behind by jvq runs that died without
// cleaning up (killed with SIGKILL, a power cut, a cancelled CI job): every
// jvq-run-* directory in the temp dir whose owner process is gone, and the
// repository's worktree records that pointed into one. It returns how many
// it removed.
func (r *Repo) CleanStale(ctx context.Context) (int, error) {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), prefix+"*"))
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, dir := range matches {
		b, err := os.ReadFile(filepath.Join(dir, ownerFile))
		if err != nil {
			// No owner file: either it is being created right now, or it
			// isn't ours to judge. Leave it.
			continue
		}
		f := strings.Fields(string(b))
		if len(f) != 2 {
			continue // not written by this version of jvq: leave it
		}
		pid, err := strconv.Atoi(f[0])
		host, _ := os.Hostname()
		if err != nil || f[1] != host || pid == os.Getpid() || alive(pid) {
			continue
		}
		if os.RemoveAll(dir) == nil {
			removed++
		}
	}
	// git forgets worktrees whose directories are gone.
	if _, err := r.Git(ctx, "worktree", "prune"); err != nil {
		return removed, err
	}
	return removed, nil
}

// Worktrees lists the paths of the repository's worktrees, the main one
// included.
func (r *Repo) Worktrees(ctx context.Context) ([]string, error) {
	out, err := r.Git(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// Short abbreviates a commit hash for reports.
func Short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
