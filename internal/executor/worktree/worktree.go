// Package worktree prepares workspaces as git worktrees of the operator's checkout.
//
// Worktrees keep runs out of each other's files, but they are not a security boundary: a run has the
// operator's full access to the machine (PLAN.md §4.4).
package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/stritech-oss/loomlc/internal/executor"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/proc"
)

// maxBackupNames bounds how many names a backup ref tries before giving up.
const maxBackupNames = 50

// refspec returns an explicit refspec that puts a branch in its remote-tracking ref, whatever the
// remote's configured refspec covers.
func refspec(remote, branch string) string {
	return "+refs/heads/" + branch + ":refs/remotes/" + remote + "/" + branch
}

// AttemptsRef is the ref namespace that keeps commits from earlier attempts on a branch.
const AttemptsRef = "refs/loomlc/attempts/"

var (
	keyPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Options configure an Executor.
type Options struct {
	// Repo is the absolute path of the operator's checkout.
	Repo string
	// Root is the directory that holds workspaces. A relative Root is relative to Repo.
	Root string
	// Remote is the git remote to fetch from, such as "origin".
	Remote string
	// Env is git's environment. It needs whatever git uses to reach the remote, such as HOME and
	// SSH_AUTH_SOCK.
	Env []string
}

// Executor prepares workspaces as git worktrees.
type Executor struct {
	git    *git.Client
	repo   string
	root   string
	remote string
	// mu serializes changes to the shared repository, such as fetches and worktree registrations.
	mu sync.Mutex
}

// New returns an Executor for the checkout at opts.Repo that runs git through runner.
func New(opts Options, runner proc.Runner) (*Executor, error) {
	if !filepath.IsAbs(opts.Repo) {
		return nil, fmt.Errorf("worktree executor: repository path %q must be absolute", opts.Repo)
	}
	if opts.Remote == "" {
		return nil, errors.New("worktree executor: a remote is required")
	}
	root := opts.Root
	if !filepath.IsAbs(root) {
		root = filepath.Join(opts.Repo, root)
	}
	return &Executor{
		git:    git.New(runner, opts.Env),
		repo:   filepath.Clean(opts.Repo),
		root:   filepath.Clean(root),
		remote: opts.Remote,
	}, nil
}

// Isolation implements the executor contract: worktrees don't isolate runs from the operator's machine.
func (e *Executor) Isolation() executor.Isolation { return executor.None }

// Prepare creates the workspace for spec from the remote's latest commits, replacing anything an earlier
// run left under the same key. Commits on the local branch that aren't on the remote are saved under
// AttemptsRef before the branch is reset.
func (e *Executor) Prepare(ctx context.Context, spec executor.Spec) (executor.Workspace, error) {
	if err := e.validate(ctx, spec); err != nil {
		return executor.Workspace{}, fmt.Errorf("prepare workspace %s: %w", spec.Key, err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	ws, err := e.prepare(ctx, spec)
	if err != nil {
		return executor.Workspace{}, fmt.Errorf("prepare workspace %s: %w", spec.Key, err)
	}
	return ws, nil
}

// Release removes the workspace. Uncommitted changes in it are discarded; its branch and commits stay in the
// repository.
func (e *Executor) Release(ctx context.Context, ws executor.Workspace) error {
	if err := e.checkInsideRoot(ws.Dir); err != nil {
		return fmt.Errorf("release workspace %s: %w", ws.Key, err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.remove(ctx, ws.Dir); err != nil {
		return fmt.Errorf("release workspace %s: %w", ws.Key, err)
	}
	return nil
}

func (e *Executor) validate(ctx context.Context, spec executor.Spec) error {
	switch {
	case !keyPattern.MatchString(spec.Key):
		return fmt.Errorf("workspace key %q must start with a lowercase letter or digit and use only lowercase letters, digits, dots, hyphens, and underscores", spec.Key)
	case !runIDPattern.MatchString(spec.RunID):
		return fmt.Errorf("run ID %q must use only letters, digits, dots, hyphens, and underscores", spec.RunID)
	case spec.Mode != executor.NewBranch && spec.Mode != executor.ExistingBranch:
		return fmt.Errorf("unknown workspace mode %d", spec.Mode)
	case spec.Base == "":
		return errors.New("a base branch is required")
	case spec.Branch == spec.Base:
		return fmt.Errorf("the run's branch can't be the base branch %q", spec.Base)
	}
	for _, branch := range []string{spec.Branch, spec.Base} {
		if _, err := e.git.Run(ctx, e.repo, "check-ref-format", "--branch", branch); err != nil {
			return fmt.Errorf("%q isn't a valid branch name: %w", branch, err)
		}
	}
	// The run id becomes part of a ref, and git's rules are stricter than the pattern above: "run.lock"
	// and "run-1." are refused. Checking here fails the first Prepare, rather than the second one — the
	// one that has an earlier attempt to save.
	backup := AttemptsRef + spec.Branch + "/" + spec.RunID
	if _, err := e.git.Run(ctx, e.repo, "check-ref-format", backup); err != nil {
		return fmt.Errorf("run ID %q can't be part of a ref name (%s): %w", spec.RunID, backup, err)
	}
	return nil
}

func (e *Executor) prepare(ctx context.Context, spec executor.Spec) (executor.Workspace, error) {
	dir := filepath.Join(e.root, spec.Key)
	baseRef := e.remote + "/" + spec.Base
	// Fetch into the remote-tracking refs by name. `git fetch <remote> <branch>` only updates them when
	// the remote's configured refspec happens to cover the branch, and a --single-branch clone — what
	// --depth implies, and what most CI checkouts are — covers exactly one. Without this the workspace
	// starts from a ref that is stale, or missing entirely.
	start, fetch := baseRef, []string{"fetch", "--quiet", e.remote, refspec(e.remote, spec.Base)}
	if spec.Mode == executor.ExistingBranch {
		start = e.remote + "/" + spec.Branch
		fetch = append(fetch, refspec(e.remote, spec.Branch))
	}

	if _, err := e.git.Run(ctx, e.repo, fetch...); err != nil {
		return executor.Workspace{}, err
	}
	if err := e.remove(ctx, dir); err != nil {
		return executor.Workspace{}, err
	}
	backup, err := e.backUpAttempt(ctx, spec, start)
	if err != nil {
		return executor.Workspace{}, err
	}

	if err := os.MkdirAll(e.root, 0o750); err != nil {
		return executor.Workspace{}, fmt.Errorf("create workspace root: %w", err)
	}
	// The operator's hooks don't run for a workspace loomlc creates. `worktree add` propagates a
	// post-checkout hook's exit status, so a hook that ends non-zero — a dependency installer, a
	// git-lfs wrapper — would fail a workspace that is in fact complete. Agents don't commit either, so
	// no hook of the operator's is skipped that would otherwise have run.
	add := []string{"-c", "core.hooksPath=" + os.DevNull, "worktree", "add", "--quiet"}
	if spec.Mode == executor.NewBranch {
		// Without tracking, a push that forgets its destination can't go to the base branch.
		add = append(add, "--no-track")
	}
	add = append(add, "-B", spec.Branch, dir, start)
	if _, err := e.git.Run(ctx, e.repo, add...); err != nil {
		return executor.Workspace{}, err
	}
	if spec.Mode == executor.NewBranch {
		// --no-track only declines to set tracking up; it doesn't clear tracking a branch of this name
		// already had, and -B keeps it. A branch left tracking the base means a bare `git push` from
		// the workspace can resolve to the base branch under push.default=upstream.
		if _, err := e.git.Run(ctx, dir, "branch", "--unset-upstream", spec.Branch); err != nil {
			var gerr *git.Error
			if !errors.As(err, &gerr) || gerr.ExitCode == 0 {
				return executor.Workspace{}, err
			}
			// Exit status alone: there was no upstream to clear, which is the ordinary case.
		}
	}

	baseSHA, err := e.git.Run(ctx, dir, "merge-base", baseRef, "HEAD")
	if err != nil {
		return executor.Workspace{}, err
	}
	return executor.Workspace{
		Key:       spec.Key,
		Dir:       dir,
		Branch:    spec.Branch,
		BaseRef:   baseRef,
		BaseSHA:   baseSHA,
		Isolation: executor.None,
		Backup:    backup,
	}, nil
}

// remove deletes whatever is at dir: a registered worktree, along with its registration, or a leftover
// directory that git doesn't know about.
//
// A worktree someone locked is left alone. git locks a worktree while it creates one, and an operator
// locks one to protect it — a removable volume, a review in progress — so "remove --force failed" can't
// be read as "not a worktree, delete the directory". Doing that destroys files the lock existed to
// protect, and leaves a registration that no longer has a directory.
func (e *Executor) remove(ctx context.Context, dir string) error {
	registered, lock, err := e.registration(ctx, dir)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(dir)
	switch {
	case statErr != nil && !errors.Is(statErr, fs.ErrNotExist):
		return fmt.Errorf("inspect workspace %s: %w", dir, statErr)

	case !registered:
		if errors.Is(statErr, fs.ErrNotExist) {
			return nil
		}
		// git doesn't know this path, so nothing of git's is thrown away by deleting it.
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove leftover workspace %s: %w", dir, err)
		}
		return nil

	case lock.locked && !errors.Is(statErr, fs.ErrNotExist):
		return fmt.Errorf("workspace %s is locked (%s); unlock it with `git worktree unlock %s` if it's finished with", dir, lock.reason, dir)

	case lock.locked:
		// Locked but the directory is gone: a leftover registration, often from an interrupted
		// `worktree add`, which git locks while it works. Nothing is lost by clearing it.
		if _, err := e.git.Run(ctx, e.repo, "worktree", "remove", "--force", "--force", dir); err != nil {
			return fmt.Errorf("clear the leftover registration for %s: %w", dir, err)
		}
		return nil

	default:
		if _, err := e.git.Run(ctx, e.repo, "worktree", "remove", "--force", dir); err != nil {
			return fmt.Errorf("remove workspace %s: %w", dir, err)
		}
		return nil
	}
}

// worktreeLock says whether a registered worktree is locked, and why.
type worktreeLock struct {
	locked bool
	reason string
}

// registration reports whether git knows dir as a worktree, and its lock. It replaces `git worktree
// prune`, which is repository-wide: pruning would discard the administrative files of any of the
// operator's own worktrees whose directory is momentarily absent, index and all.
func (e *Executor) registration(ctx context.Context, dir string) (bool, worktreeLock, error) {
	out, err := e.git.Run(ctx, e.repo, "worktree", "list", "--porcelain")
	if err != nil {
		return false, worktreeLock{}, fmt.Errorf("list worktrees: %w", err)
	}

	want, err := filepath.Abs(dir)
	if err != nil {
		return false, worktreeLock{}, fmt.Errorf("resolve workspace %s: %w", dir, err)
	}
	found := false
	var lock worktreeLock
	for line := range strings.SplitSeq(out, "\n") {
		switch field, value, _ := strings.Cut(strings.TrimSpace(line), " "); {
		case field == "worktree":
			if found {
				return true, lock, nil // the next entry started; this one had no lock line
			}
			path, err := filepath.Abs(value)
			found = err == nil && path == want
		case found && field == "locked":
			return true, worktreeLock{locked: true, reason: lockReason(value)}, nil
		}
	}
	return found, lock, nil
}

// lockReason describes a lock for an error message. git records a lock with no reason as a bare line.
func lockReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "no reason given"
	}
	return reason
}

// backUpAttempt saves the local branch's commits that aren't in start, so that resetting the branch doesn't
// lose an earlier attempt's work. It returns the backup ref, or "" when there was nothing to save.
func (e *Executor) backUpAttempt(ctx context.Context, spec executor.Spec, start string) (string, error) {
	local := "refs/heads/" + spec.Branch
	if _, err := e.git.Run(ctx, e.repo, "rev-parse", "--verify", "--quiet", local); err != nil {
		var gerr *git.Error
		if errors.As(err, &gerr) && gerr.ExitCode == 1 {
			return "", nil // no local branch yet
		}
		return "", err
	}

	unpushed, err := e.git.Run(ctx, e.repo, "rev-list", "--count", start+".."+local)
	if err != nil || unpushed == "0" {
		return "", err
	}
	// update-ref overwrites, and a ref outside refs/heads gets no reflog by default, so writing the same
	// name twice — a run that prepares its workspace more than once under one run id — would leave the
	// earlier attempt's commits unreachable and eventually collected. The backup is created, never
	// replaced, and takes the next free name when something is already there.
	base := AttemptsRef + spec.Branch + "/" + spec.RunID
	for attempt := 1; attempt <= maxBackupNames; attempt++ {
		backup := base
		if attempt > 1 {
			backup = fmt.Sprintf("%s.%d", base, attempt)
		}
		// An empty old value means "only if it doesn't exist yet".
		_, err := e.git.Run(ctx, e.repo, "update-ref", backup, local, "")
		if err == nil {
			return backup, nil
		}
		var gerr *git.Error
		if !errors.As(err, &gerr) {
			return "", err
		}
	}
	return "", fmt.Errorf("back up the earlier attempt on %s: %s and %d names after it are taken", spec.Branch, base, maxBackupNames-1)
}

// checkInsideRoot refuses to act on directories outside the workspace root.
func (e *Executor) checkInsideRoot(dir string) error {
	rel, err := filepath.Rel(e.root, filepath.Clean(dir))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%q isn't a workspace under %s", dir, e.root)
	}
	return nil
}
