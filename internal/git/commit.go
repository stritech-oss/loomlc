package git

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Commit is one commit on a branch.
type Commit struct {
	SHA     string
	Subject string
}

// Fixup reports whether this commit is a fix waiting to be folded into another.
func (c Commit) Fixup() bool {
	return strings.HasPrefix(c.Subject, "fixup! ") || strings.HasPrefix(c.Subject, "squash! ")
}

// Head returns the commit the workspace is on.
func (c *Client) Head(ctx context.Context, dir string) (string, error) {
	return c.Run(ctx, dir, "rev-parse", "HEAD")
}

// Tree returns the hash of the tree at HEAD, for comparing a rewrite against what it started from.
func (c *Client) Tree(ctx context.Context, dir string) (string, error) {
	return c.Run(ctx, dir, "rev-parse", "HEAD^{tree}")
}

// Dirty reports whether the workspace has uncommitted changes, ignored files aside.
func (c *Client) Dirty(ctx context.Context, dir string) (bool, error) {
	out, err := c.Run(ctx, dir, "status", "--porcelain")
	return out != "", err
}

// ChangedPaths returns the paths an agent has changed and nobody has committed, including files it added
// and files it deleted.
func (c *Client) ChangedPaths(ctx context.Context, dir string) ([]string, error) {
	out, err := c.Run(ctx, dir, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var paths []string
	for entry := range strings.SplitSeq(out, "\x00") {
		// Each entry is "XY <path>"; -z keeps paths whole, however they're spelled.
		if len(entry) > 3 {
			paths = append(paths, entry[3:])
		}
	}
	return paths, nil
}

// Log returns the commits in base..HEAD, oldest first.
func (c *Client) Log(ctx context.Context, dir, base string) ([]Commit, error) {
	out, err := c.Run(ctx, dir, "log", "--reverse", "--format=%H%x00%s", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var commits []Commit
	for line := range strings.SplitSeq(out, "\n") {
		sha, subject, ok := strings.Cut(line, "\x00")
		if !ok {
			return nil, fmt.Errorf("git log: unreadable line %q", line)
		}
		commits = append(commits, Commit{SHA: sha, Subject: subject})
	}
	return commits, nil
}

// CommitPaths commits exactly paths with message, signed off by the committer. Other changes in the
// workspace are left alone, so one agent answer can become several commits.
//
// An empty commit and a path git doesn't know are errors: both mean the caller asked for something that
// doesn't match the workspace, which the engine reports as a finding rather than papering over.
func (c *Client) CommitPaths(ctx context.Context, dir, message string, paths []string) (string, error) {
	if len(paths) == 0 {
		return "", fmt.Errorf("commit %q: no paths", subject(message))
	}
	if err := c.stage(ctx, dir, paths); err != nil {
		return "", err
	}
	args := append([]string{"commit", "--signoff", "--message", message, "--"}, paths...)
	if _, err := c.Run(ctx, dir, args...); err != nil {
		return "", fmt.Errorf("commit %q: %w", subject(message), err)
	}
	return c.Head(ctx, dir)
}

// CommitFixup records a fix to an earlier commit, so a reviewer sees what changed. why explains the fix;
// git keeps it until the fixup is folded, and folding drops it.
func (c *Client) CommitFixup(ctx context.Context, dir, target, why string, paths []string) (string, error) {
	if len(paths) == 0 {
		return "", fmt.Errorf("fix %s: no paths", short(target))
	}
	if err := c.stage(ctx, dir, paths); err != nil {
		return "", err
	}
	args := []string{"commit", "--signoff", "--fixup=" + target}
	if why != "" {
		args = append(args, "--message", why)
	}
	args = append(args, "--")
	args = append(args, paths...)
	if _, err := c.Run(ctx, dir, args...); err != nil {
		return "", fmt.Errorf("fix %s: %w", short(target), err)
	}
	return c.Head(ctx, dir)
}

// Autosquash folds every fixup commit in base..HEAD into the commit it names.
//
// git exits 0 when it folds nothing — a target outside base..HEAD is silently left alone — so success is
// judged by the result: no fixup commits left, and the tree exactly as it was.
func (c *Client) Autosquash(ctx context.Context, dir, base string) error {
	before, err := c.Tree(ctx, dir)
	if err != nil {
		return err
	}
	// git 2.43 needs -i for --autosquash, and an editor it won't open. Last duplicate wins, so these go
	// at the end.
	env := append(slices.Clone(c.env), "GIT_SEQUENCE_EDITOR=:", "GIT_EDITOR=:")
	if _, err := c.runWith(ctx, env, dir, "rebase", "--interactive", "--autosquash", base); err != nil {
		if _, abortErr := c.runWith(ctx, env, dir, "rebase", "--abort"); abortErr != nil {
			return fmt.Errorf("fold fixups: %w; and the rebase couldn't be aborted: %w", err, abortErr)
		}
		return fmt.Errorf("fold fixups: %w", err)
	}

	commits, err := c.Log(ctx, dir, base)
	if err != nil {
		return err
	}
	for _, commit := range commits {
		if commit.Fixup() {
			return fmt.Errorf("fold fixups: %s is still a fixup; it names a commit outside %s..HEAD", short(commit.SHA), base)
		}
	}
	after, err := c.Tree(ctx, dir)
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("fold fixups: the tree changed from %s to %s; the branch is no longer what was reviewed", short(before), short(after))
	}
	return nil
}

// ResetSoft moves the branch to rev and keeps the workspace, for taking back a commit an agent made
// itself and remaking it through the engine.
func (c *Client) ResetSoft(ctx context.Context, dir, rev string) error {
	_, err := c.Run(ctx, dir, "reset", "--soft", rev)
	return err
}

// Discard throws away everything uncommitted, including files git doesn't track.
func (c *Client) Discard(ctx context.Context, dir string) error {
	if _, err := c.Run(ctx, dir, "reset", "--hard"); err != nil {
		return err
	}
	_, err := c.Run(ctx, dir, "clean", "-fdx")
	return err
}

// stage records the paths' current state, so a commit covers additions and deletions alike.
func (c *Client) stage(ctx context.Context, dir string, paths []string) error {
	args := append([]string{"add", "--all", "--"}, paths...)
	if _, err := c.Run(ctx, dir, args...); err != nil {
		return fmt.Errorf("stage %s: %w", strings.Join(paths, ", "), err)
	}
	return nil
}

// subject is a message's first line, for an error that has to name the commit.
func subject(message string) string {
	line, _, _ := strings.Cut(message, "\n")
	return line
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
