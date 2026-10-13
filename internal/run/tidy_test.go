package run

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stritech-oss/loomlc/internal/executor"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/sink"
)

// branches is the sink a fold reads the pull request from and reports to.
type branches struct {
	output   sink.Output
	outErr   error
	comments []string
	err      error
}

func (b *branches) Output(context.Context, sink.Ref) (sink.Output, error) {
	return b.output, b.outErr
}

func (b *branches) CommentOn(ctx context.Context, _ sink.Ref, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.comments = append(b.comments, body)
	return b.err
}

// folding is the git a fold runs through: a branch with fixups on it, and the history it comes out as.
type folding struct {
	before, after []git.Commit
	// head is what the remote is on, which is the commit the push should lease.
	head    string
	foldErr error
	folded  bool
	pushErr error
	// lease records the force-push: the remote, the branch, and the commit it was allowed to replace.
	lease  []string
	pushes int
}

func (f *folding) Log(context.Context, string, string) ([]git.Commit, error) {
	if f.folded {
		return f.after, nil
	}
	return f.before, nil
}

// Head answers what the workspace is on, which changes when the fold rewrites it: the lease has to be
// taken from the commit the remote had, before anything was folded.
func (f *folding) Head(context.Context, string) (string, error) {
	if f.folded && len(f.after) > 0 {
		return f.after[len(f.after)-1].SHA, nil
	}
	return f.head, nil
}

func (f *folding) Autosquash(ctx context.Context, _, _ string) error {
	// A rebase that is killed fails like any other, which is what the cancelled context stands for.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fold fixups: %w; and the rebase couldn't be aborted: %w", err, err)
	}
	if f.foldErr != nil {
		return f.foldErr
	}
	f.folded = true
	return nil
}

func (f *folding) ForcePushWithLease(_ context.Context, _, remote, branch, expected string) error {
	f.pushes++
	f.lease = []string{remote, branch, expected}
	return f.pushErr
}

// reviewed is a branch as a feedback run left it: the commit the reviewer read, and a fix to it.
func reviewed() *folding {
	return &folding{
		head: "2222222222222222",
		before: []git.Commit{
			{SHA: "1111111111111111", Subject: "feat(cli): print the build commit"},
			{SHA: "2222222222222222", Subject: "fixup! feat(cli): print the build commit"},
		},
		after: []git.Commit{{SHA: "3333333333333333", Subject: "feat(cli): print the build commit"}},
	}
}

// tidier builds a fold of a pull request that has one fixup waiting on it.
func tidier(t *testing.T, edit ...func(*branches, *folding, *policy, *Tidier)) (*Tidier, *branches, *folding, *workspaces, *policy) {
	t.Helper()
	out := &branches{output: proposed()}
	vcs, pol, spaces := reviewed(), &policy{}, &workspaces{}
	tr := &Tidier{
		Lifecycle: "sdlc",
		Remote:    "origin",
		Now:       func() time.Time { return time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC) },
		Progress:  &strings.Builder{},
	}
	for _, edit := range edit {
		edit(out, vcs, pol, tr)
	}
	tr.Outputs, tr.Workspaces, tr.VCS, tr.Policy, tr.Text = out, spaces, vcs, pol, text{}
	return tr, out, vcs, spaces, pol
}

// progress is what the operator's terminal was told, which is where the detail of a refusal goes
// instead of onto the pull request.
func progress(tr *Tidier) string { return tr.Progress.(*strings.Builder).String() }

func pr() sink.Ref { return sink.Ref{Sink: "github-pr", ID: "44"} }

func TestTidyFoldsTheFixupsAndSaysWhatTheBranchIsNow(t *testing.T) {
	tr, out, vcs, spaces, pol := tidier(t)

	res, err := tr.Tidy(context.Background(), pr())
	if err != nil {
		t.Fatalf("Tidy: %v", err)
	}

	if !res.Folded() || res.Fixups != 1 {
		t.Fatalf("tidy = %+v, want one fixup folded and pushed", res)
	}
	if !vcs.folded {
		t.Error("nothing was folded")
	}
	// The push leases the commit the remote was on, so a push that landed while this was folding stops
	// it rather than being overwritten.
	if want := []string{"origin", "feat/issue-9-print-the-build-commit", "2222222222222222"}; !slices.Equal(vcs.lease, want) {
		t.Errorf("force-push = %q, want %q", vcs.lease, want)
	}
	// The fold works in a workspace of the pull request's own, on the branch the remote already has.
	if spaces.spec.Key != "github-pr-44" || spaces.spec.Mode != executor.ExistingBranch {
		t.Errorf("workspace = %+v, want the pull request's workspace on the existing branch", spaces.spec)
	}
	if spaces.spec.Branch != "feat/issue-9-print-the-build-commit" || spaces.spec.Base != "main" {
		t.Errorf("workspace = %+v, want the pull request's branch and base", spaces.spec)
	}
	if len(spaces.released) != 1 {
		t.Errorf("released %v, want the workspace given back", spaces.released)
	}
	// A folded branch has no fixups left, so it is held to the rule that gates the merge.
	if len(pol.modes) != 1 || pol.modes[0] != "range" {
		t.Errorf("policy modes = %q, want the one that refuses fixups", pol.modes)
	}

	if len(out.comments) != 1 {
		t.Fatalf("comments = %q, want one", out.comments)
	}
	for _, want := range []string{
		sink.TidyMarker,
		"One fix went into the commit it belonged to.",
		"**Before**",
		"`111111111111` feat(cli): print the build commit",
		"`222222222222` fixup! feat(cli): print the build commit",
		"**Now**",
		"`333333333333` feat(cli): print the build commit",
		"force-pushed",
	} {
		if !strings.Contains(out.comments[0], want) {
			t.Errorf("the comment doesn't contain %q:\n%s", want, out.comments[0])
		}
	}
}

// A branch that is already logical commits is where tidy is trying to get to, so running it again does
// nothing — and says nothing on the pull request, since an operator who tidies before every merge
// shouldn't leave a comment on each one that needed none.
func TestTidyLeavesABranchWithNothingToFoldAlone(t *testing.T) {
	tr, out, vcs, _, pol := tidier(t, func(_ *branches, f *folding, _ *policy, _ *Tidier) {
		f.before = []git.Commit{{SHA: "1111111111111111", Subject: "feat(cli): print the build commit"}}
	})

	res, err := tr.Tidy(context.Background(), pr())
	if err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	if res.Idle == "" || res.Folded() {
		t.Errorf("tidy = %+v, want it idle", res)
	}
	if vcs.folded || vcs.pushes > 0 {
		t.Error("a branch with no fixups was rewritten")
	}
	if len(out.comments) > 0 {
		t.Errorf("comments = %q, want none", out.comments)
	}
	if len(pol.modes) > 0 {
		t.Errorf("policy modes = %q, want none: nothing was folded to check", pol.modes)
	}
}

// Nothing is touched on a pull request loomlc has no business rewriting, and no workspace is even
// prepared for one.
func TestTidyRefusesPullRequestsItCannotPushTo(t *testing.T) {
	tests := []struct {
		name   string
		output sink.Output
		want   string
	}{
		{"closed", func() sink.Output { o := proposed(); o.Open = false; return o }(), "is closed"},
		{"from a fork", func() sink.Output { o := proposed(); o.CrossRepo = true; return o }(), "comes from a fork"},
		{"no branch", func() sink.Output { o := proposed(); o.Branch = ""; return o }(), "names no branch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, out, vcs, spaces, _ := tidier(t, func(b *branches, _ *folding, _ *policy, _ *Tidier) {
				b.output = tt.output
			})

			res, err := tr.Tidy(context.Background(), pr())
			if err != nil {
				t.Fatalf("Tidy: %v", err)
			}
			// A refusal rather than an idle run: the operator named this pull request, and a script
			// that folds before merging has to be able to tell the two apart.
			if res.Idle != "" || res.Folded() {
				t.Errorf("tidy = %+v, want a refusal", res)
			}
			if len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0], tt.want) {
				t.Errorf("refusals = %q, want one saying %q", res.Refusals, tt.want)
			}
			if spaces.spec.Key != "" || vcs.folded {
				t.Errorf("workspace = %+v, folded = %v: want nothing touched", spaces.spec, vcs.folded)
			}
			if len(out.comments) > 0 {
				t.Errorf("comments = %q, want none", out.comments)
			}
		})
	}
}

// Fixups that don't apply, or a fold that changes the tree, leave the branch for a person to fold by
// hand. Nothing is pushed, and the pull request is told so.
func TestTidyDoesNotPushAFoldThatDidNotApply(t *testing.T) {
	tr, out, vcs, _, pol := tidier(t, func(_ *branches, f *folding, _ *policy, _ *Tidier) {
		f.foldErr = errors.New("fold fixups: the tree changed from 1111111 to 4444444")
	})

	res, err := tr.Tidy(context.Background(), pr())
	if err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	if res.Folded() || vcs.pushes > 0 {
		t.Fatalf("tidy = %+v, pushes = %d, want nothing pushed", res, vcs.pushes)
	}
	if len(res.Refusals) != 1 {
		t.Fatalf("refusals = %q, want the one that says why", res.Refusals)
	}
	if len(pol.modes) > 0 {
		t.Errorf("policy modes = %q, want none: there is no folded history to check", pol.modes)
	}
	if len(out.comments) != 1 {
		t.Fatalf("comments = %q, want the pull request told", out.comments)
	}
	for _, want := range []string{
		"couldn't fold the fixups",
		"didn't fold into the commits they name",
		"git rebase -i --autosquash abc1234",
	} {
		if !strings.Contains(out.comments[0], want) {
			t.Errorf("the comment doesn't contain %q:\n%s", want, out.comments[0])
		}
	}
	// git's own account of it goes to the operator, not onto the pull request: it carries whatever git
	// printed, and the rules that keep attribution out of what loomlc posts are anchored to the start
	// of a line, so a trailer inside it would sail past them.
	if strings.Contains(out.comments[0], "the tree changed") {
		t.Errorf("the comment repeats git's output:\n%s", out.comments[0])
	}
	if !strings.Contains(progress(tr), "the tree changed") {
		t.Errorf("the operator wasn't told what git said:\n%s", progress(tr))
	}
}

// Interrupting a fold is not the branch's fault. A killed rebase fails like a conflicting one, and
// saying on the pull request that its fixups don't fold would be a permanent public claim loomlc has no
// evidence for.
func TestTidySaysNothingOnThePullRequestWhenItIsInterrupted(t *testing.T) {
	tr, out, vcs, _, _ := tidier(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tr.Tidy(ctx, pr())
	if err == nil {
		t.Fatal("Tidy reported no error for a fold that was interrupted")
	}
	if res.Pushed || vcs.pushes > 0 {
		t.Errorf("tidy = %+v, pushes = %d, want nothing pushed", res, vcs.pushes)
	}
	if len(res.Refusals) > 0 {
		t.Errorf("refusals = %q, want none: the branch refused nothing", res.Refusals)
	}
	if len(out.comments) > 0 {
		t.Errorf("comments = %q, want none", out.comments)
	}
}

// The commits that come out of a fold are history nobody has reviewed, so they go through the policy
// before they reach the remote.
func TestTidyDoesNotPushWhatTheCommitPolicyRefuses(t *testing.T) {
	tr, out, vcs, _, _ := tidier(t, func(_ *branches, _ *folding, p *policy, _ *Tidier) {
		p.err = errors.New("commit 3333333: missing a human Signed-off-by")
	})

	res, err := tr.Tidy(context.Background(), pr())
	if err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	if res.Folded() || vcs.pushes > 0 {
		t.Fatalf("tidy = %+v, pushes = %d, want nothing pushed", res, vcs.pushes)
	}
	if len(out.comments) != 1 || !strings.Contains(out.comments[0], "don't pass the commit policy") {
		t.Errorf("comments = %q, want it to say the policy refused them", out.comments)
	}
	// The checker quotes the commits it refused, which on this branch can be anybody's: a contributor's
	// address out of a Signed-off-by, or the very trailer the policy exists to keep out of what loomlc
	// posts. Its output goes to the operator's terminal and no further.
	if strings.Contains(out.comments[0], "missing a human Signed-off-by") {
		t.Errorf("the comment repeats the checker's output:\n%s", out.comments[0])
	}
	if !strings.Contains(progress(tr), "missing a human Signed-off-by") {
		t.Errorf("the operator wasn't told what the checker said:\n%s", progress(tr))
	}
}

// A refused fold whose comment the policy then refuses still has to say that something stopped it, or
// the reader is left with a heading and nothing under it.
func TestTidyKeepsTheRefusalInTheShorterComment(t *testing.T) {
	tr, out, _, _, _ := tidier(t, func(_ *branches, f *folding, _ *policy, _ *Tidier) {
		f.foldErr = errors.New("fold fixups: CONFLICT in version.txt")
	})
	tr.Text = text{bans: "didn't fold into the commits they name"}

	if _, err := tr.Tidy(context.Background(), pr()); err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	if len(out.comments) != 1 {
		t.Fatalf("comments = %q, want one", out.comments)
	}
	if !strings.Contains(out.comments[0], "**Why**") || !strings.Contains(out.comments[0], "operator's terminal") {
		t.Errorf("the shorter comment doesn't say that something stopped it:\n%s", out.comments[0])
	}
}

// A push that fails leaves the pull request exactly as it was, and nothing of the attempt on it: there
// is nothing there to explain, and the error is the operator's to read.
func TestTidySaysNothingOnThePullRequestWhenThePushFails(t *testing.T) {
	tr, out, _, _, _ := tidier(t, func(_ *branches, f *folding, _ *policy, _ *Tidier) {
		f.pushErr = errors.New("stale info: refs/heads/feat/issue-9 has moved")
	})

	res, err := tr.Tidy(context.Background(), pr())
	if err == nil {
		t.Fatal("Tidy reported no error for a push that failed")
	}
	if res.Pushed || res.Commented {
		t.Errorf("tidy = %+v, want nothing pushed and nothing said", res)
	}
	if len(out.comments) > 0 {
		t.Errorf("comments = %q, want none", out.comments)
	}
}

// The comment quotes the branch's commit subjects, and a fixup's subject is git's own prefix in front of
// whatever the commit it names was called. The policy's rules are anchored to the start of a line, so a
// trailer behind that prefix is the sanitizer's to catch.
func TestTidyDropsAttributionFromTheCommitsItLists(t *testing.T) {
	tr, out, _, _, _ := tidier(t, func(_ *branches, f *folding, _ *policy, _ *Tidier) {
		f.before[1].Subject = "fixup! Co-authored-by: Claude <noreply@anthropic.com>"
	})

	if _, err := tr.Tidy(context.Background(), pr()); err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	if len(out.comments) != 1 {
		t.Fatalf("comments = %q, want one", out.comments)
	}
	if strings.Contains(out.comments[0], "Co-authored-by") {
		t.Errorf("the comment carries attribution:\n%s", out.comments[0])
	}
}

// A comment the policy still refuses is cut back to loomlc's own words rather than left unsaid.
func TestTidyFallsBackToACommentThePolicyAccepts(t *testing.T) {
	tr, out, _, _, _ := tidier(t)
	tr.Text = text{bans: "print the build commit"}

	if _, err := tr.Tidy(context.Background(), pr()); err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	if len(out.comments) != 1 {
		t.Fatalf("comments = %q, want one", out.comments)
	}
	if strings.Contains(out.comments[0], "**Before**") {
		t.Errorf("the comment kept the lists the policy refused:\n%s", out.comments[0])
	}
	if !strings.Contains(out.comments[0], "One fix went into the commit it belonged to.") {
		t.Errorf("the comment lost what loomlc itself had to say:\n%s", out.comments[0])
	}
}

func TestTidyNeedsTheThingsItWorksWith(t *testing.T) {
	tr, _, _, _, _ := tidier(t)

	if _, err := tr.Tidy(context.Background(), sink.Ref{Sink: "github-pr"}); err == nil {
		t.Error("Tidy folded a pull request it wasn't named")
	}

	tr.VCS, tr.Remote = nil, ""
	_, err := tr.Tidy(context.Background(), pr())
	if err == nil {
		t.Fatal("Tidy ran without a git client or a remote")
	}
	for _, want := range []string{"git client", "remote"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name the missing %s", err, want)
		}
	}
}
