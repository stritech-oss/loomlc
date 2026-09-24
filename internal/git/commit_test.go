package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/proc"
)

// repo is a real repository in a temporary directory, with git's own configuration ignored so the tests
// behave the same on every machine.
type repo struct {
	t   *testing.T
	dir string
	git *Client
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	dir := t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Ada Human",
		"GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada Human",
		"GIT_COMMITTER_EMAIL=ada@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	}
	r := &repo{t: t, dir: dir, git: New(proc.Exec{}, env)}
	r.run("init", "--quiet", "--initial-branch=main", ".")
	r.write("base.txt", "base\n")
	r.run("add", "base.txt")
	r.run("commit", "--quiet", "--message", "chore: start")
	return r
}

func (r *repo) run(args ...string) string {
	r.t.Helper()
	out, err := r.git.Run(context.Background(), r.dir, args...)
	if err != nil {
		r.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func (r *repo) write(name, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(content), 0o600); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
}

// base is the first commit, which every test uses as the point its work starts after.
func (r *repo) base() string { return r.run("rev-list", "--max-parents=0", "HEAD") }

// subjects returns the subjects of base..HEAD, oldest first.
func (r *repo) subjects(base string) []string {
	r.t.Helper()
	commits, err := r.git.Log(context.Background(), r.dir, base)
	if err != nil {
		r.t.Fatalf("Log: %v", err)
	}
	var out []string
	for _, c := range commits {
		out = append(out, c.Subject)
	}
	return out
}

func TestCommitPathsTakesOnlyThePathsItIsGiven(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	r.write("a.txt", "a\n")
	r.write("b.txt", "b\n")

	sha, err := r.git.CommitPaths(ctx, r.dir, "feat: add a", []string{"a.txt"})
	if err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	if len(sha) != 40 {
		t.Errorf("sha = %q, want a full hash", sha)
	}
	if got := r.run("show", "--name-only", "--format=", "HEAD"); got != "a.txt" {
		t.Errorf("commit contains %q, want only a.txt", got)
	}
	if got := r.run("status", "--porcelain"); got != "?? b.txt" {
		t.Errorf("left uncommitted: %q, want b.txt untouched", got)
	}
	if !strings.Contains(r.run("log", "-1", "--format=%B"), "Signed-off-by: Ada Human") {
		t.Error("the commit has no sign-off")
	}
}

func TestCommitPathsRecordsAdditionsAndDeletions(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	r.write("added.txt", "new\n")
	if err := os.Remove(filepath.Join(r.dir, "base.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, err := r.git.CommitPaths(ctx, r.dir, "feat: swap the files", []string{"added.txt", "base.txt"}); err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	got := r.run("show", "--name-status", "--format=", "HEAD")
	for _, want := range []string{"A\tadded.txt", "D\tbase.txt"} {
		if !strings.Contains(got, want) {
			t.Errorf("commit shows %q, want it to contain %q", got, want)
		}
	}
}

// Both of these mean the caller described a change the workspace doesn't have, which the engine reports
// rather than papering over.
func TestCommitPathsRefusesWhatDoesNotMatchTheWorkspace(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()

	if _, err := r.git.CommitPaths(ctx, r.dir, "feat: nothing changed", []string{"base.txt"}); err == nil {
		t.Error("committing an unchanged path succeeded, want an error")
	}
	if _, err := r.git.CommitPaths(ctx, r.dir, "feat: a file nobody wrote", []string{"ghost.txt"}); err == nil {
		t.Error("committing a path git doesn't know succeeded, want an error")
	}
	if _, err := r.git.CommitPaths(ctx, r.dir, "feat: no paths at all", nil); err == nil {
		t.Error("committing with no paths succeeded, want an error")
	}
}

func TestChangedPathsSeesEveryKindOfChange(t *testing.T) {
	r := newRepo(t)
	r.write("tracked.txt", "one\n")
	r.run("add", "tracked.txt")
	r.run("commit", "--quiet", "--message", "chore: add tracked")

	r.write("tracked.txt", "two\n")
	r.write("untracked file.txt", "new\n") // a space, to prove the parsing doesn't split on one
	if err := os.Remove(filepath.Join(r.dir, "base.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	paths, err := r.git.ChangedPaths(context.Background(), r.dir)
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	want := map[string]bool{"tracked.txt": true, "untracked file.txt": true, "base.txt": true}
	if len(paths) != len(want) {
		t.Fatalf("paths = %q, want %d entries", paths, len(want))
	}
	for _, p := range paths {
		if !want[p] {
			t.Errorf("unexpected path %q", p)
		}
	}
}

func TestFixupsFoldIntoTheirTargetAndLeaveTheTreeAlone(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	base := r.base()

	r.write("a.txt", "one\n")
	target, err := r.git.CommitPaths(ctx, r.dir, "feat: add a", []string{"a.txt"})
	if err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	// A commit between the fix and its target, which is the case a naive fold gets wrong.
	r.write("b.txt", "b\n")
	if _, err := r.git.CommitPaths(ctx, r.dir, "test: cover a", []string{"b.txt"}); err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	r.write("a.txt", "fixed\n")
	if _, err := r.git.CommitFixup(ctx, r.dir, target, "a was wrong", []string{"a.txt"}); err != nil {
		t.Fatalf("CommitFixup: %v", err)
	}

	if got := r.subjects(base); strings.Join(got, "|") != "feat: add a|test: cover a|fixup! feat: add a" {
		t.Fatalf("before folding: %q", got)
	}
	if !strings.Contains(r.run("log", "-1", "--format=%B"), "a was wrong") {
		t.Error("the fixup doesn't carry its explanation, which is what a reviewer reads")
	}

	before, err := r.git.Tree(ctx, r.dir)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if err := r.git.Autosquash(ctx, r.dir, base); err != nil {
		t.Fatalf("Autosquash: %v", err)
	}
	if got := r.subjects(base); strings.Join(got, "|") != "feat: add a|test: cover a" {
		t.Errorf("after folding: %q, want the fixup folded into its target", got)
	}
	after, err := r.git.Tree(ctx, r.dir)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if before != after {
		t.Errorf("tree changed from %s to %s; folding must not change what was reviewed", before, after)
	}
}

// git exits 0 when a fixup's target is outside the range it rebases, having folded nothing. Reporting
// success there would push a branch whose history still says "fixup!".
func TestAutosquashRefusesWhenItFoldsNothing(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	target := r.run("rev-parse", "HEAD") // the base commit itself

	r.write("base.txt", "changed\n")
	if _, err := r.git.CommitFixup(ctx, r.dir, target, "fixing the base commit", []string{"base.txt"}); err != nil {
		t.Fatalf("CommitFixup: %v", err)
	}

	err := r.git.Autosquash(ctx, r.dir, target)
	if err == nil {
		t.Fatal("Autosquash reported success having folded nothing")
	}
	if !strings.Contains(err.Error(), "still a fixup") {
		t.Errorf("error = %v, want it to say the fixup is still there", err)
	}
}

func TestAutosquashAbortsAConflictAndSaysSo(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	base := r.base()

	r.write("a.txt", "one\n")
	target, err := r.git.CommitPaths(ctx, r.dir, "feat: add a", []string{"a.txt"})
	if err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	// A later commit rewrites the same line, so folding the fix into the earlier commit can't apply.
	r.write("a.txt", "rewritten\n")
	if _, err := r.git.CommitPaths(ctx, r.dir, "refactor: rewrite a", []string{"a.txt"}); err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	r.write("a.txt", "one, fixed\n")
	if _, err := r.git.CommitFixup(ctx, r.dir, target, "fixing the first version", []string{"a.txt"}); err != nil {
		t.Fatalf("CommitFixup: %v", err)
	}

	if err := r.git.Autosquash(ctx, r.dir, base); err == nil {
		t.Fatal("Autosquash succeeded through a conflict")
	}
	// The workspace is usable afterwards: no rebase left half-done.
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Error("a rebase was left in progress; the workspace is stuck")
	}
	if got := r.run("rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("HEAD is %q, want the branch back", got)
	}
}

func TestResetSoftKeepsTheWork(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	before := r.run("rev-parse", "HEAD")
	r.write("a.txt", "a\n")
	r.run("add", "a.txt")
	r.run("commit", "--quiet", "--message", "feat: a commit an agent made itself")

	if err := r.git.ResetSoft(ctx, r.dir, before); err != nil {
		t.Fatalf("ResetSoft: %v", err)
	}
	if got := r.run("rev-parse", "HEAD"); got != before {
		t.Errorf("HEAD = %s, want %s", got, before)
	}
	if got := r.run("status", "--porcelain"); !strings.Contains(got, "a.txt") {
		t.Errorf("status = %q, want the work still there to commit properly", got)
	}
}

func TestDiscardAndDirty(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	r.write("base.txt", "changed\n")
	r.write("extra.txt", "new\n")

	dirty, err := r.git.Dirty(ctx, r.dir)
	if err != nil || !dirty {
		t.Fatalf("Dirty = %v, %v; want true", dirty, err)
	}
	if err := r.git.Discard(ctx, r.dir); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	dirty, err = r.git.Dirty(ctx, r.dir)
	if err != nil || dirty {
		t.Errorf("Dirty after Discard = %v, %v; want false", dirty, err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "extra.txt")); !os.IsNotExist(err) {
		t.Error("Discard left an untracked file behind")
	}
}

func TestLogReportsCommitsOldestFirst(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	base := r.base()
	for _, name := range []string{"one", "two"} {
		r.write(name+".txt", name+"\n")
		if _, err := r.git.CommitPaths(ctx, r.dir, "feat: add "+name, []string{name + ".txt"}); err != nil {
			t.Fatalf("CommitPaths: %v", err)
		}
	}

	commits, err := r.git.Log(ctx, r.dir, base)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 2 || commits[0].Subject != "feat: add one" || commits[1].Subject != "feat: add two" {
		t.Errorf("commits = %+v, want add one then add two", commits)
	}
	if commits[0].Fixup() {
		t.Error("an ordinary commit reports itself as a fixup")
	}

	if empty, err := r.git.Log(ctx, r.dir, "HEAD"); err != nil || empty != nil {
		t.Errorf("Log with nothing in range = %v, %v; want none", empty, err)
	}
}
