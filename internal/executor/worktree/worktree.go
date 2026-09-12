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
	return nil
}

func (e *Executor) prepare(ctx context.Context, spec executor.Spec) (executor.Workspace, error) {
	dir := filepath.Join(e.root, spec.Key)
	baseRef := e.remote + "/" + spec.Base
	start, fetch := baseRef, []string{"fetch", "--quiet", e.remote, spec.Base}
	if spec.Mode == executor.ExistingBranch {
		start = e.remote + "/" + spec.Branch
		fetch = append(fetch, spec.Branch)
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
	add := []string{"worktree", "add", "--quiet"}
	if spec.Mode == executor.NewBranch {
		// Without tracking, a push that forgets its destination can't go to the base branch.
		add = append(add, "--no-track")
	}
	add = append(add, "-B", spec.Branch, dir, start)
	if _, err := e.git.Run(ctx, e.repo, add...); err != nil {
		return executor.Workspace{}, err
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

// remove deletes whatever is at dir, whether a registered worktree or a leftover directory, and clears
// registrations of worktrees whose directories are gone.
func (e *Executor) remove(ctx context.Context, dir string) error {
	if _, err := os.Stat(dir); err == nil {
		if _, err := e.git.Run(ctx, e.repo, "worktree", "remove", "--force", dir); err != nil {
			// Not a registered worktree: the directory is a leftover inside loomlc's own root.
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("remove leftover workspace %s: %w", dir, err)
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect workspace %s: %w", dir, err)
	}
	_, err := e.git.Run(ctx, e.repo, "worktree", "prune")
	return err
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
	backup := AttemptsRef + spec.Branch + "/" + spec.RunID
	if _, err := e.git.Run(ctx, e.repo, "update-ref", backup, local); err != nil {
		return "", err
	}
	return backup, nil
}

// checkInsideRoot refuses to act on directories outside the workspace root.
func (e *Executor) checkInsideRoot(dir string) error {
	rel, err := filepath.Rel(e.root, filepath.Clean(dir))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%q isn't a workspace under %s", dir, e.root)
	}
	return nil
}
