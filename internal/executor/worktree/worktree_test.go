package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stritech-oss/loomlc/internal/executor"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/proc"
)

// fixture is a bare origin repository, the operator's checkout of it, and a second clone that pushes changes
// the way a teammate or GitHub would. Everything is local to a temporary directory, and git's own
// configuration is ignored so the tests behave the same on every machine.
type fixture struct {
	t      *testing.T
	env    []string
	origin string
	repo   string
	other  string
	exec   *Executor
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	f := &fixture{
		t: t,
		env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + base,
			"GIT_CONFIG_GLOBAL=" + os.DevNull,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Ada Human",
			"GIT_AUTHOR_EMAIL=ada@example.com",
			"GIT_COMMITTER_NAME=Ada Human",
			"GIT_COMMITTER_EMAIL=ada@example.com",
			"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
			"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		},
		origin: filepath.Join(base, "origin.git"),
		repo:   filepath.Join(base, "repo"),
		other:  filepath.Join(base, "other"),
	}

	f.git(base, "init", "--quiet", "--bare", "--initial-branch=main", f.origin)
	f.git(base, "clone", "--quiet", f.origin, f.repo)
	f.git(f.repo, "symbolic-ref", "HEAD", "refs/heads/main")
	f.commit(f.repo, "README.md", "hello\n", "docs: start")
	f.git(f.repo, "push", "--quiet", "origin", "main")
	f.git(base, "clone", "--quiet", f.origin, f.other)

	exec, err := New(Options{Repo: f.repo, Root: ".loomlc/worktrees", Remote: "origin", Env: f.env}, proc.Exec{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.exec = exec
	return f
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	out, err := git.New(proc.Exec{}, f.env).Run(context.Background(), dir, args...)
	if err != nil {
		f.t.Fatalf("%v", err)
	}
	return out
}

// commit writes content to file in dir, commits it, and returns the commit's hash.
func (f *fixture) commit(dir, file, content, message string) string {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		f.t.Fatalf("write %s: %v", file, err)
	}
	f.git(dir, "add", file)
	f.git(dir, "commit", "--quiet", "-m", message)
	return f.git(dir, "rev-parse", "HEAD")
}

func (f *fixture) prepare(spec executor.Spec) executor.Workspace {
	f.t.Helper()
	ws, err := f.exec.Prepare(context.Background(), spec)
	if err != nil {
		f.t.Fatalf("Prepare: %v", err)
	}
	return ws
}

// newBranch returns a spec for a first run on a new branch; tests that model later runs change its RunID.
func newBranch(key, branch string) executor.Spec {
	return executor.Spec{Key: key, RunID: "run-1", Branch: branch, Base: "main", Mode: executor.NewBranch}
}

func TestPrepareStartsANewBranchFromTheLatestBase(t *testing.T) {
	f := newFixture(t)
	latest := f.commit(f.other, "CHANGELOG.md", "pushed after the checkout was cloned\n", "docs: add changelog")
	f.git(f.other, "push", "--quiet", "origin", "main")

	ws := f.prepare(newBranch("issue-42", "feat/issue-42-add-config"))

	if want := filepath.Join(f.repo, ".loomlc", "worktrees", "issue-42"); ws.Dir != want {
		t.Errorf("Dir = %q, want %q", ws.Dir, want)
	}
	checks := []struct{ name, got, want string }{
		{"branch", f.git(ws.Dir, "rev-parse", "--abbrev-ref", "HEAD"), "feat/issue-42-add-config"},
		{"HEAD", f.git(ws.Dir, "rev-parse", "HEAD"), latest},
		{"BaseSHA", ws.BaseSHA, latest},
		{"BaseRef", ws.BaseRef, "origin/main"},
		{"Backup", ws.Backup, ""},
		{"Isolation", ws.Isolation.String(), "none"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if _, err := git.New(proc.Exec{}, f.env).Run(context.Background(), ws.Dir, "rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		t.Error("the new branch tracks an upstream; it must not, so a bare push can't reach the base branch")
	}
}

func TestPrepareChecksOutAnExistingRemoteBranch(t *testing.T) {
	f := newFixture(t)
	base := f.git(f.other, "rev-parse", "HEAD")
	f.git(f.other, "switch", "--quiet", "-c", "feat/issue-7-fix")
	head := f.commit(f.other, "fix.txt", "fixed\n", "fix: handle the edge case")
	f.git(f.other, "push", "--quiet", "origin", "feat/issue-7-fix")

	ws := f.prepare(executor.Spec{Key: "pr-17-feedback", RunID: "run-1", Branch: "feat/issue-7-fix", Base: "main", Mode: executor.ExistingBranch})

	if got := f.git(ws.Dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD = %s, want the pushed branch %s", got, head)
	}
	if ws.BaseSHA != base {
		t.Errorf("BaseSHA = %s, want where the branch forked from main, %s", ws.BaseSHA, base)
	}
}

func TestPrepareFailsForABranchMissingFromTheRemote(t *testing.T) {
	f := newFixture(t)

	_, err := f.exec.Prepare(context.Background(), executor.Spec{Key: "pr-3-feedback", RunID: "run-1", Branch: "feat/gone", Base: "main", Mode: executor.ExistingBranch})
	if err == nil || !strings.Contains(err.Error(), "feat/gone") {
		t.Fatalf("error = %v, want it to name the missing branch", err)
	}
}

func TestPrepareReplacesWhatAnEarlierRunLeftBehind(t *testing.T) {
	f := newFixture(t)
	spec := newBranch("issue-42", "feat/issue-42-add-config")

	first := f.prepare(spec)
	if err := os.WriteFile(filepath.Join(first.Dir, "scratch.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatalf("write scratch file: %v", err)
	}
	spec.RunID = "run-2"
	second := f.prepare(spec)
	if _, err := os.Stat(filepath.Join(second.Dir, "scratch.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("uncommitted file from the earlier run survived: %v", err)
	}

	if err := f.exec.Release(context.Background(), second); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(second.Dir, "junk"), 0o750); err != nil {
		t.Fatalf("create leftover directory: %v", err)
	}
	spec.RunID = "run-3"
	third := f.prepare(spec)
	if _, err := os.Stat(filepath.Join(third.Dir, "junk")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("leftover directory survived: %v", err)
	}
}

func TestPrepareBacksUpCommitsFromAnEarlierAttempt(t *testing.T) {
	f := newFixture(t)
	spec := newBranch("issue-42", "feat/issue-42-add-config")

	first := f.prepare(spec)
	attempt := f.commit(first.Dir, "config.go", "package config\n", "feat(config): first attempt")

	spec.RunID = "run-2"
	second := f.prepare(spec)

	if want := "refs/loomlc/attempts/feat/issue-42-add-config/run-2"; second.Backup != want {
		t.Fatalf("Backup = %q, want %q", second.Backup, want)
	}
	if got := f.git(f.repo, "rev-parse", second.Backup); got != attempt {
		t.Errorf("backup points at %s, want the earlier attempt's commit %s", got, attempt)
	}
	if got := f.git(second.Dir, "rev-parse", "HEAD"); got != second.BaseSHA {
		t.Errorf("HEAD = %s, want the branch reset to the base %s", got, second.BaseSHA)
	}
}

func TestReleaseRemovesTheWorkspaceButKeepsItsBranch(t *testing.T) {
	f := newFixture(t)
	ws := f.prepare(newBranch("issue-42", "feat/issue-42-add-config"))
	commit := f.commit(ws.Dir, "config.go", "package config\n", "feat(config): add package")

	if err := f.exec.Release(context.Background(), ws); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(ws.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("workspace directory still exists: %v", err)
	}
	if strings.Contains(f.git(f.repo, "worktree", "list"), ws.Dir) {
		t.Error("the worktree is still registered")
	}
	if got := f.git(f.repo, "rev-parse", "refs/heads/feat/issue-42-add-config"); got != commit {
		t.Errorf("branch = %s, want it kept at %s", got, commit)
	}

	if err := f.exec.Release(context.Background(), ws); err != nil {
		t.Errorf("releasing an already released workspace: %v", err)
	}
}

func TestReleaseRefusesDirectoriesOutsideTheRoot(t *testing.T) {
	f := newFixture(t)
	for _, dir := range []string{f.repo, filepath.Join(f.repo, ".loomlc", "worktrees"), filepath.Join(f.repo, ".loomlc", "worktrees", "..", "..", "src")} {
		err := f.exec.Release(context.Background(), executor.Workspace{Key: "issue-1", Dir: dir})
		if err == nil || !strings.Contains(err.Error(), "isn't a workspace under") {
			t.Errorf("Release(%q) = %v, want a refusal", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.repo, "README.md")); err != nil {
		t.Errorf("the checkout was damaged: %v", err)
	}
}

func TestPrepareRejectsInvalidSpecs(t *testing.T) {
	f := newFixture(t)
	valid := newBranch("issue-1", "feat/issue-1")
	tests := []struct {
		name    string
		edit    func(*executor.Spec)
		wantErr string
	}{
		{name: "key with a slash", edit: func(s *executor.Spec) { s.Key = "../escape" }, wantErr: "workspace key"},
		{name: "run ID with a slash", edit: func(s *executor.Spec) { s.RunID = "a/b" }, wantErr: "run ID"},
		{name: "unknown mode", edit: func(s *executor.Spec) { s.Mode = 0 }, wantErr: "unknown workspace mode"},
		{name: "no base", edit: func(s *executor.Spec) { s.Base = "" }, wantErr: "base branch is required"},
		{name: "branch is the base", edit: func(s *executor.Spec) { s.Branch = "main" }, wantErr: "can't be the base branch"},
		{name: "invalid branch name", edit: func(s *executor.Spec) { s.Branch = "feat..bad" }, wantErr: "isn't a valid branch name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := valid
			tt.edit(&spec)
			if _, err := f.exec.Prepare(context.Background(), spec); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestPrepareHandlesConcurrentRuns(t *testing.T) {
	f := newFixture(t)

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 1; i <= 4; i++ {
		wg.Go(func() {
			_, err := f.exec.Prepare(context.Background(), newBranch(fmt.Sprintf("issue-%d", i), fmt.Sprintf("feat/issue-%d", i)))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Prepare: %v", err)
		}
	}
	if n := strings.Count(f.git(f.repo, "worktree", "list"), "\n") + 1; n != 5 {
		t.Errorf("registered worktrees = %d, want the checkout plus 4", n)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	if _, err := New(Options{Repo: "relative/repo", Remote: "origin"}, proc.Exec{}); err == nil {
		t.Error("New accepted a relative repository path")
	}
	if _, err := New(Options{Repo: "/repo"}, proc.Exec{}); err == nil {
		t.Error("New accepted an empty remote")
	}
}

// A worktree someone locked is protected, not deleted. git locks one while it creates it, and an
// operator locks one to keep it — so "remove failed" must not be read as "not a worktree, delete it".
func TestPrepareRefusesToDestroyALockedWorkspace(t *testing.T) {
	f := newFixture(t)
	ws := f.prepare(newBranch("issue-42", "feat/issue-42"))
	precious := filepath.Join(ws.Dir, "uncommitted.txt")
	if err := os.WriteFile(precious, []byte("hours of work\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.git(f.repo, "worktree", "lock", "--reason", "review in progress", ws.Dir)

	_, err := f.exec.Prepare(context.Background(), newBranch("issue-42", "feat/issue-42"))
	if err == nil {
		t.Fatal("Prepare succeeded on a locked workspace, want it refused")
	}
	if !strings.Contains(err.Error(), "review in progress") {
		t.Errorf("error = %v, want it to name the lock reason", err)
	}
	if _, err := os.Stat(precious); err != nil {
		t.Errorf("the locked workspace's files were deleted anyway: %v", err)
	}
}

// A locked registration whose directory is gone is a leftover — an interrupted `worktree add` leaves
// exactly that — and clearing it is what frees the key again.
func TestPrepareClearsALeftoverLockedRegistration(t *testing.T) {
	f := newFixture(t)
	ws := f.prepare(newBranch("issue-42", "feat/issue-42"))
	f.git(f.repo, "worktree", "lock", "--reason", "initializing", ws.Dir)
	if err := os.RemoveAll(ws.Dir); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, err := f.exec.Prepare(context.Background(), newBranch("issue-42", "feat/issue-42")); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
}

// Pruning is repository-wide, so it used to discard the administrative files of any worktree of the
// operator's whose directory was momentarily absent — a removable volume, a directory being moved.
func TestPrepareLeavesTheOperatorsOwnWorktreesAlone(t *testing.T) {
	f := newFixture(t)
	portable := filepath.Join(t.TempDir(), "portable")
	f.git(f.repo, "worktree", "add", "--quiet", "-b", "operator/wip", portable)
	f.commit(portable, "staged.txt", "work\n", "feat: operator work")
	moved := portable + "-unmounted"
	if err := os.Rename(portable, moved); err != nil {
		t.Fatalf("move the operator's worktree away: %v", err)
	}

	f.prepare(newBranch("issue-42", "feat/issue-42"))

	if err := os.Rename(moved, portable); err != nil {
		t.Fatalf("move it back: %v", err)
	}
	if out := f.git(portable, "rev-parse", "--abbrev-ref", "HEAD"); out != "operator/wip" {
		t.Errorf("the operator's worktree reports %q; its registration was pruned", out)
	}
}

// update-ref overwrites, and refs outside refs/heads keep no reflog, so a run that prepared twice under
// one id used to leave the first attempt's commits unreachable.
func TestBackupRefsDoNotOverwriteEachOther(t *testing.T) {
	f := newFixture(t)
	ws := f.prepare(newBranch("issue-42", "feat/issue-42"))
	first := f.commit(ws.Dir, "one.txt", "1\n", "feat: attempt one")

	spec := newBranch("issue-42", "feat/issue-42") // the same run id again
	ws = f.prepare(spec)
	second := f.commit(ws.Dir, "two.txt", "2\n", "feat: attempt two")
	f.prepare(spec)

	for _, sha := range []string{first, second} {
		out := f.git(f.repo, "for-each-ref", "--contains", sha, "--format=%(refname)", AttemptsRef)
		if strings.TrimSpace(out) == "" {
			t.Errorf("no backup ref reaches %s; that attempt's commits are unreachable", sha[:7])
		}
	}
}

// A run id that passes the key pattern can still be impossible as part of a ref. Failing on the first
// Prepare beats failing on the second, which is the one with an earlier attempt to save.
func TestPrepareRejectsRunIDsThatCannotFormARef(t *testing.T) {
	f := newFixture(t)
	for _, runID := range []string{"run.lock", "run-1.", "a..b"} {
		t.Run(runID, func(t *testing.T) {
			spec := newBranch("issue-42", "feat/issue-42")
			spec.RunID = runID
			if _, err := f.exec.Prepare(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "ref name") {
				t.Errorf("Prepare = %v, want it refused for a run id that can't be part of a ref", err)
			}
		})
	}
}

// -B keeps whatever upstream the branch already had, and --no-track only declines to add one. A branch
// left tracking the base means a bare push from the workspace can resolve to the base branch.
func TestPrepareClearsAnUpstreamTheBranchAlreadyHad(t *testing.T) {
	f := newFixture(t)
	f.git(f.repo, "branch", "--track", "feat/issue-42", "origin/main")
	if out := f.git(f.repo, "rev-parse", "--abbrev-ref", "feat/issue-42@{upstream}"); out != "origin/main" {
		t.Fatalf("test setup: upstream = %q, want origin/main", out)
	}

	ws := f.prepare(newBranch("issue-42", "feat/issue-42"))

	if _, err := git.New(proc.Exec{}, f.env).Run(context.Background(), ws.Dir, "rev-parse", "--abbrev-ref", "feat/issue-42@{upstream}"); err == nil {
		t.Error("the branch still tracks the base; a bare push could go there")
	}
}

// A post-checkout hook's exit status fails `worktree add`, unlike `git checkout`. The workspace is
// complete either way, and an operator whose hook ends non-zero could never run loomlc.
func TestPrepareIsNotFailedByTheOperatorsHooks(t *testing.T) {
	f := newFixture(t)
	hooks := filepath.Join(f.repo, ".git", "hooks")
	if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	ws := f.prepare(newBranch("issue-42", "feat/issue-42"))
	if out := f.git(ws.Dir, "rev-parse", "--abbrev-ref", "HEAD"); out != "feat/issue-42" {
		t.Errorf("workspace is on %q, want feat/issue-42", out)
	}
}

// `git fetch <remote> <branch>` only updates the remote-tracking ref when the remote's configured
// refspec covers the branch. A --single-branch clone covers exactly one, which is what CI checkouts are.
func TestPrepareFetchesIntoRemoteTrackingRefsInASingleBranchClone(t *testing.T) {
	f := newFixture(t)
	// A teammate's branch, pushed after this clone was made narrow.
	f.git(f.repo, "checkout", "--quiet", "-b", "feat/issue-7")
	f.commit(f.repo, "theirs.txt", "theirs\n", "feat: their work")
	f.git(f.repo, "push", "--quiet", "origin", "feat/issue-7")
	f.git(f.repo, "checkout", "--quiet", "main")

	narrow := filepath.Join(t.TempDir(), "narrow")
	f.git(t.TempDir(), "clone", "--quiet", "--single-branch", "--branch", "main", f.origin, narrow)
	exec, err := New(Options{Repo: narrow, Root: ".loomlc/worktrees", Remote: "origin", Env: f.env}, proc.Exec{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	spec := executor.Spec{Key: "issue-7", RunID: "run-1", Branch: "feat/issue-7", Base: "main", Mode: executor.ExistingBranch}
	ws, err := exec.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatalf("Prepare in a single-branch clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Dir, "theirs.txt")); err != nil {
		t.Errorf("the workspace doesn't have the branch's latest commit: %v", err)
	}
}
