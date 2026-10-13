package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/stritech-oss/loomlc/internal/executor"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/sink"
)

// Branches is what folding needs from the sink: which branch a pull request is, and somewhere to say
// what happened to it.
type Branches interface {
	Output(ctx context.Context, ref sink.Ref) (sink.Output, error)
	CommentOn(ctx context.Context, ref sink.Ref, body string) error
}

// Folds is what folding needs from git.
type Folds interface {
	Log(ctx context.Context, dir, base string) ([]git.Commit, error)
	Head(ctx context.Context, dir string) (string, error)
	Autosquash(ctx context.Context, dir, base string) error
	ForcePushWithLease(ctx context.Context, dir, remote, branch, expected string) error
}

// Range checks a branch's commits against the commit policy with nothing tolerated, which is the check
// a folded branch has to pass: the fixups a feedback push left are gone, so the rule that gates the
// merge applies again.
type Range interface {
	CheckRange(ctx context.Context, dir, base, head string) error
}

// Tidier folds the fixup commits on one pull request's branch into the commits they fix.
//
// It is the only thing in loomlc that rewrites a branch a reviewer has already read, which is why a
// person runs it and nothing in a run reaches it. It changes which commits the branch has and not what
// they add up to: the fold is refused unless the tree comes out byte-identical, so what was reviewed is
// what gets merged.
type Tidier struct {
	// Lifecycle names the lifecycle whose pull requests this folds, for what it prints.
	Lifecycle string
	// Remote is the git remote the pull request's branch lives on.
	Remote string

	Outputs    Branches
	Workspaces Workspaces
	VCS        Folds
	Policy     Range
	// Text checks what tidy posts: the comment quotes the branch's commit subjects, which are an
	// agent's words and sometimes a stranger's.
	Text Text

	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Progress receives a line as the fold goes, for an operator watching. Nil is silent.
	Progress io.Writer
}

// Tidy is what folding did to one pull request, for an operator's terminal, a script's exit code, and
// the comment on the pull request.
type Tidy struct {
	// Output is the pull request that was folded.
	Output sink.Ref
	URL    string
	// Base is the commit the branch's own commits start after, which is what the fold rebases onto and
	// what folding by hand has to rebase onto too.
	Base string
	// Idle says there was nothing to fold.
	Idle string
	// Before and After are the branch's commits, oldest first, as the reviewer read them and as they
	// are now.
	Before, After []git.Commit
	// Fixups is how many commits were waiting to be folded.
	Fixups int
	// Pushed says the folded history reached the pull request's branch.
	Pushed bool
	// Refusals say why it didn't. They are loomlc's own words: whatever git or the policy printed goes
	// to the operator's terminal, not into a public comment.
	Refusals []string
	// Commented says the pull request was told what happened to it.
	Commented bool
}

// Folded reports whether the branch is logical commits again: the fixups went into the commits they
// name, and the result is what the pull request now holds.
func (t Tidy) Folded() bool { return t.Pushed && len(t.Refusals) == 0 }

// Tidy folds the fixup commits on the branch of the pull request ref names and pushes the result.
//
// An error means the fold didn't finish: either nothing was pushed, or it was pushed and something
// afterwards failed, which the Tidy says which of. A fold loomlc refused comes back as a Tidy saying
// why, with the reason already on the pull request.
func (t *Tidier) Tidy(ctx context.Context, ref sink.Ref) (Tidy, error) {
	if err := t.valid(); err != nil {
		return Tidy{}, err
	}
	if ref.ID == "" {
		return Tidy{}, fmt.Errorf("tidy %s: name the pull request whose fixups to fold", t.Lifecycle)
	}

	out, err := t.Outputs.Output(ctx, ref)
	if err != nil {
		return Tidy{}, err
	}
	res := Tidy{Output: ref, URL: out.URL}
	// Nothing is touched on a pull request loomlc has no business rewriting: a closed one has had its
	// decision made, and one from a fork is somebody else's branch. This is a refusal rather than an
	// idle run, because the operator asked for this pull request by name: a script that folds before
	// merging has to be able to tell "there was nothing to fold" from "loomlc wouldn't fold it".
	// Nothing is said on the pull request either, since loomlc changed nothing about it.
	if reason := unwritable(out, "fold"); reason != "" {
		res.Refusals = []string{ref.String() + " " + reason}
		t.say("%s", res.Refusals[0])
		return res, nil
	}
	return res, t.fold(ctx, out, &res)
}

// fold is the work: the pull request's branch in a workspace of its own, the fixups folded into their
// targets, the commit policy over what came out, and then the push and the comment. It fills in the
// result as it goes, so what it learned survives an error on the next line.
func (t *Tidier) fold(ctx context.Context, out sink.Output, res *Tidy) error {
	ws, err := t.Workspaces.Prepare(ctx, executor.Spec{
		Key:    outputKey(res.Output),
		RunID:  t.runID(res.Output),
		Branch: out.Branch,
		Base:   out.Base,
		Mode:   executor.ExistingBranch,
	})
	if err != nil {
		return err
	}
	defer t.release(ctx, ws)
	res.Base = ws.BaseSHA
	t.say("working in %s on %s", ws.Dir, ws.Branch)
	if ws.Backup != "" {
		// Preparing the workspace reset the local branch to the remote's. Commits that were only on the
		// local one are saved, and the operator is the only one who can tell whether they wanted them.
		t.say("commits that were only on the local branch are saved at %s", ws.Backup)
	}

	before, err := t.VCS.Log(ctx, ws.Dir, ws.BaseSHA)
	if err != nil {
		return err
	}
	res.Before, res.Fixups = before, fixups(before)
	if res.Fixups == 0 {
		// Nothing is said on the pull request. A branch of logical commits is the state tidy exists to
		// reach, and an operator who runs this before every merge shouldn't leave a comment on each
		// pull request that already needed nothing.
		res.Idle = res.Output.String() + " has no fixup commits to fold"
		t.say("%s", res.Idle)
		return nil
	}
	// What the remote is on, which is what the push leases. Preparing the workspace has just reset the
	// branch to it, so this is the remote's own tip rather than whatever a local branch held.
	leased, err := t.VCS.Head(ctx, ws.Dir)
	if err != nil {
		return err
	}
	t.say("folding %d of %d commits into the commits they fix; the remote is at %s", res.Fixups, len(before), short(leased))

	if err := t.VCS.Autosquash(ctx, ws.Dir, ws.BaseSHA); err != nil {
		if stopped(ctx, err) {
			return err
		}
		// A fold that doesn't apply is a fact about the branch rather than a broken command: the
		// fixups conflict with each other, or one names a commit that isn't on the branch, or folding
		// them changed the tree. Nothing is pushed in any of those cases, so the pull request is as
		// the reviewer left it and a person has to fold it by hand.
		t.say("the fixups didn't fold: %v", err)
		res.Refusals = append(res.Refusals, "the fixups didn't fold into the commits they name; git's own account of it is in the operator's terminal")
		return t.tell(ctx, res)
	}
	// The branch is history nobody has reviewed yet, so it is checked as a branch that is ready to
	// merge: the fixups are gone, and this is the gate they were holding up.
	if err := t.Policy.CheckRange(ctx, ws.Dir, ws.BaseSHA, "HEAD"); err != nil {
		if stopped(ctx, err) {
			return err
		}
		t.say("the folded commits don't pass the commit policy: %v", err)
		res.Refusals = append(res.Refusals, "the folded commits don't pass the commit policy; what it said is in the operator's terminal")
		return t.tell(ctx, res)
	}
	after, err := t.VCS.Log(ctx, ws.Dir, ws.BaseSHA)
	if err != nil {
		return err
	}
	res.After = after

	if err := t.VCS.ForcePushWithLease(ctx, ws.Dir, t.Remote, ws.Branch, leased); err != nil {
		// Either the remote moved while this was folding, or the push failed outright. The pull request
		// still has its fixups and nothing of this attempt is on it, so there is nothing to explain
		// there: the error is the operator's to read.
		return err
	}
	res.Pushed = true
	t.say("pushed %d commits to %s, replacing the %d that were there", len(after), ws.Branch, len(before))
	return t.tell(ctx, res)
}

// tell says on the pull request what the fold did, because the branch a reviewer was reading has just
// changed under them — or hasn't, and they are the ones who have to fold it.
func (t *Tidier) tell(ctx context.Context, res *Tidy) error {
	ctx, cancel := cleanup(ctx)
	defer cancel()

	said := *res
	before, dropped := sanitizeCommits(said.Before)
	after, goneFromAfter := sanitizeCommits(said.After)
	said.Before, said.After = before, after
	dropped = append(dropped, goneFromAfter...)
	if len(dropped) > 0 {
		t.say("dropped attribution from the comment: %s", strings.Join(dropped, "; "))
	}

	body := Tidied(said)
	if err := t.Text.CheckText(ctx, body); err != nil {
		t.say("the comment didn't pass the commit policy, so it says less: %v", err)
		short := Tidy{Output: said.Output, Base: said.Base, Fixups: said.Fixups, Pushed: said.Pushed}
		if len(said.Refusals) > 0 {
			short.Refusals = []string{"The reason is in the operator's terminal: it couldn't be repeated here."}
		}
		body = Tidied(short)
		if err := t.Text.CheckText(ctx, body); err != nil {
			return fmt.Errorf("say what was folded on %s: %w", said.Output, err)
		}
	}
	if err := t.Outputs.CommentOn(ctx, said.Output, body); err != nil {
		return fmt.Errorf("say what was folded on %s: %w", said.Output, err)
	}
	res.Commented = true
	return nil
}

// release gives the workspace back. Its branch stays in the repository, so a fold that wasn't pushed is
// still there to look at.
func (t *Tidier) release(ctx context.Context, ws executor.Workspace) {
	ctx, cancel := cleanup(ctx)
	defer cancel()
	if err := t.Workspaces.Release(ctx, ws); err != nil {
		t.say("the workspace at %s is still there: %v", ws.Dir, err)
	}
}

// stopped reports whether the run was interrupted rather than the branch refusing the fold. A killed
// rebase fails like any other, and saying on a pull request that its fixups conflict would be a
// permanent public claim about somebody's branch that loomlc has no evidence for.
func stopped(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// sanitizeCommits drops agent attribution from each commit's subject. The commits themselves stay: the
// list says what the branch holds, so cleaning it removes words, never a commit.
func sanitizeCommits(commits []git.Commit) ([]git.Commit, []string) {
	out := slices.Clone(commits)
	var dropped []string
	for i, c := range out {
		clean, gone := sanitizeSubject(c.Subject)
		out[i].Subject, dropped = clean, append(dropped, gone...)
	}
	return out, dropped
}

// outputKey is one workspace key per output, so folding a pull request's fixups works somewhere of its
// own rather than in the workspace the task's own runs use. It is lowercased because that is the
// charset an executor allows in a key.
func outputKey(ref sink.Ref) string {
	return strings.ToLower(ref.Sink + "-" + ref.ID)
}

func (t *Tidier) runID(ref sink.Ref) string {
	return t.now().UTC().Format("20060102-150405.000") + "-" + outputKey(ref)
}

func (t *Tidier) now() time.Time { return now(t.Now) }

func (t *Tidier) say(format string, args ...any) { say(t.Progress, format, args...) }

func (t *Tidier) valid() error {
	var missing []string
	for _, part := range []struct {
		name string
		got  bool
	}{
		{"sink", t.Outputs != nil},
		{"executor", t.Workspaces != nil},
		{"git client", t.VCS != nil},
		{"commit policy checker", t.Policy != nil},
		{"text checker", t.Text != nil},
		{"remote", t.Remote != ""},
	} {
		if !part.got {
			missing = append(missing, part.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("tidy %s: the tidier was built without a %s", t.Lifecycle, strings.Join(missing, ", a "))
	}
	return nil
}
