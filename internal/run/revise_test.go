package run

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stritech-oss/loomlc/internal/executor"
	"github.com/stritech-oss/loomlc/internal/gate"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/lifecycle"
	"github.com/stritech-oss/loomlc/internal/prompt"
	"github.com/stritech-oss/loomlc/internal/sink"
	"github.com/stritech-oss/loomlc/internal/task"
)

// reviews is the sink a feedback run reads its review from and answers on.
type reviews struct {
	next     []sink.Ref
	output   sink.Output
	items    []sink.FeedbackItem
	taskRef  task.Ref
	hasTask  bool
	claimed  []sink.Ref
	released []sink.Ref
	comments []string
	// errs fails one named call, for a forge that is up for the first call and down for the next.
	errs map[string]error
}

func (r *reviews) NextFeedback(context.Context, []sink.Ref) (sink.Ref, bool, error) {
	if err := r.errs["NextFeedback"]; err != nil {
		return sink.Ref{}, false, err
	}
	if len(r.next) == 0 {
		return sink.Ref{}, false, nil
	}
	return r.next[0], true, nil
}

func (r *reviews) Output(context.Context, sink.Ref) (sink.Output, error) {
	return r.output, r.errs["Output"]
}

func (r *reviews) Feedback(context.Context, sink.Ref) ([]sink.FeedbackItem, error) {
	return r.items, r.errs["Feedback"]
}

func (r *reviews) TaskFor(context.Context, sink.Ref) (task.Ref, bool, error) {
	return r.taskRef, r.hasTask, r.errs["TaskFor"]
}

func (r *reviews) ClaimFeedback(_ context.Context, ref sink.Ref) error {
	r.claimed = append(r.claimed, ref)
	return r.errs["ClaimFeedback"]
}

// ReleaseFeedback refuses a cancelled context, the way gh does once the run is interrupted.
func (r *reviews) ReleaseFeedback(ctx context.Context, ref sink.Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.released = append(r.released, ref)
	return r.errs["ReleaseFeedback"]
}

func (r *reviews) CommentOn(ctx context.Context, _ sink.Ref, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.comments = append(r.comments, body)
	return r.errs["CommentOn"]
}

// revisions is the lifecycle runner in its feedback mode.
type revisions struct {
	spec   lifecycle.Spec
	result lifecycle.Result
	err    error
}

func (r *revisions) Revise(_ context.Context, spec lifecycle.Spec) (lifecycle.Result, error) {
	r.spec = spec
	return r.result, r.err
}

// proposed is the pull request under review: open, on this repository, with a branch to check out.
func proposed() sink.Output {
	return sink.Output{
		Ref:    sink.Ref{Sink: "github-pr", ID: "44"},
		URL:    "https://github.com/acme/widgets/pull/44",
		Branch: "feat/issue-9-print-the-build-commit",
		Base:   "main",
		Open:   true,
	}
}

func comments() []sink.FeedbackItem {
	return []sink.FeedbackItem{{
		ID: "RC_1", Kind: "inline", Author: "ada", Path: "a.go", Line: 12,
		Body:      "The flag is read twice here.",
		CreatedAt: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC),
	}}
}

// revised is a revision that answered the comment with a fixup.
func revised() lifecycle.Result {
	return lifecycle.Result{
		Outcome: lifecycle.Proposed,
		Passes:  1,
		Change: prompt.Change{
			Status:    "complete",
			Summary:   "Read the flag once.",
			Addressed: []prompt.Addressed{{Item: "The flag is read twice", Response: "Read it once and kept the value."}},
		},
		Verdict: prompt.Verdict{Verdict: "pass", Summary: "task check passes."},
		Commits: []git.Commit{{SHA: "2222222222222222", Subject: "fixup! feat(cli): print the build commit"}},
		Gates:   []gate.Result{{Name: "task check", Passed: true}},
	}
}

// pieces are a revision's fakes, kept together so a test can read what any of them recorded.
type pieces struct {
	reviews *reviews
	tasks   *source
	spaces  *workspaces
	steps   *revisions
	log     *runLog
	vcs     *vcs
	checks  *checks
	pusher  *pusher
	policy  Text
}

// reviser builds a feedback run on an open pull request that has one comment to answer.
func reviser(t *testing.T, edit ...func(*pieces, *Reviser)) (*Reviser, *pieces) {
	t.Helper()
	p := &pieces{
		reviews: &reviews{
			next:    []sink.Ref{{Sink: "github-pr", ID: "44"}},
			output:  proposed(),
			items:   comments(),
			taskRef: task.Ref{Source: "github", ID: "9"},
			hasTask: true,
			errs:    map[string]error{},
		},
		tasks:  &source{got: ready()},
		spaces: &workspaces{},
		steps:  &revisions{result: revised()},
		log:    &runLog{},
		vcs:    work(),
		checks: &checks{results: []gate.Result{{Name: "task check", Passed: true}}},
		pusher: &pusher{},
		policy: text{},
	}
	r := &Reviser{
		Lifecycle:  lifecycle.Spec{Name: "sdlc", MaxPasses: 3, ProtectedPaths: []string{"loomlc.yml"}},
		Publishing: Options{ProtectedPaths: []string{"loomlc.yml"}, Gates: [][]string{{"task", "check"}}, Verified: true},
		Remote:     "origin",
		Now:        func() time.Time { return time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC) },
	}
	for _, edit := range edit {
		edit(p, r)
	}
	r.Reviews, r.Tasks, r.Workspaces, r.Steps = p.reviews, p.tasks, p.spaces, p.steps
	r.Publisher = New(p.vcs, p.checks, &policy{}, nil)
	r.Push, r.Policy, r.NewLog = p.pusher, p.policy, p.log.opened
	return r, p
}

func TestRevisePushesTheAnswerAndRepliesToTheReviewer(t *testing.T) {
	r, p := reviser(t)

	rev, err := r.Revise(context.Background(), sink.Ref{})
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}

	if !rev.Pushed || !rev.Replied || rev.Fixups != 1 {
		t.Fatalf("revision = %+v, want it pushed, answered, and one fixup counted", rev)
	}
	if p.pusher.branch != "feat/issue-9-print-the-build-commit" {
		t.Errorf("pushed %q, want the pull request's own branch", p.pusher.branch)
	}
	// What the reviewer has read stays as they read it: the fixups are pushed on top, not folded in.
	if p.vcs.folded {
		t.Error("the revision rewrote the history the reviewer had already read")
	}
	if len(p.reviews.claimed) != 1 || len(p.reviews.released) != 1 {
		t.Errorf("claimed %v and released %v, want each once", p.reviews.claimed, p.reviews.released)
	}
	// The workspace is the task's, checked out from the branch that is already on the remote.
	if p.spaces.spec.Key != "github-9" || p.spaces.spec.Mode != executor.ExistingBranch {
		t.Errorf("workspace = %+v, want the task's workspace on the existing branch", p.spaces.spec)
	}
	if p.spaces.spec.Branch != "feat/issue-9-print-the-build-commit" || p.spaces.spec.Base != "main" {
		t.Errorf("workspace = %+v, want the pull request's branch and base", p.spaces.spec)
	}
	if len(p.spaces.released) != 1 {
		t.Errorf("released %v, want the workspace given back", p.spaces.released)
	}
	// The steps get the feedback and the task the change was built for.
	if len(p.steps.spec.Feedback) != 1 || p.steps.spec.Task.Title != ready().Title {
		t.Errorf("spec = %+v, want the feedback and the task it revises", p.steps.spec)
	}

	reply := p.reviews.comments[0]
	for _, want := range []string{
		sink.ReplyMarker,
		"The branch has been updated.",
		"**The flag is read twice** — Read it once and kept the value.",
		"`222222222222` fixup! feat(cli): print the build commit",
		"git rebase -i --autosquash main",
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("the reply doesn't contain %q:\n%s", want, reply)
		}
	}
}

func TestReviseRecordsWhatItDid(t *testing.T) {
	r, p := reviser(t)

	rev, err := r.Revise(context.Background(), sink.Ref{})
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}

	record := p.log.last(t)
	if record.ID != "20261012-093000.000-github-pr-44" || rev.Log != ".loomlc/runs/"+record.ID {
		t.Errorf("record = %+v, want a run id naming the time and the pull request", record)
	}
	if record.Flow != "feedback" {
		t.Errorf("flow = %q, want the record to say which flow this was", record.Flow)
	}
	if record.Output == nil || record.Output.Ref != "github-pr#44" {
		t.Errorf("record.Output = %+v, want the pull request it revised", record.Output)
	}
	if record.Task.Ref != "github#9" || record.Outcome != "proposed" || len(record.Commits) != 1 {
		t.Errorf("record = %+v, want the task, the outcome and the commit", record)
	}
}

// A reviewer who asked for changes gets an answer either way, and the labels come off so a watch doesn't
// pick the same pull request up again.
func TestReviseAnswersAndReleasesWhateverHappens(t *testing.T) {
	tests := map[string]struct {
		edit func(*pieces, *Reviser)
		want string
	}{
		"a step was blocked": {
			edit: func(p *pieces, _ *Reviser) {
				p.steps.result = lifecycle.Result{
					Outcome:  lifecycle.Blocked,
					Passes:   1,
					Blockers: []string{"the comment asks for a rename across a protected path"},
				}
			},
			want: "A step stopped",
		},
		"the review never passed": {
			edit: func(p *pieces, _ *Reviser) {
				p.steps.result = lifecycle.Result{Outcome: lifecycle.Exhausted, Passes: 3, Blockers: []string{"the test still fails"}}
			},
			want: "didn't pass review in 3 passes",
		},
		"the push was refused": {
			edit: func(p *pieces, _ *Reviser) {
				p.vcs.paths = []string{"loomlc.yml"}
			},
			want: "wouldn't push it",
		},
		"nothing needed to change": {
			edit: func(p *pieces, _ *Reviser) {
				result := revised()
				result.Commits = nil
				p.steps.result = result
			},
			want: "Nothing on the branch needed to change",
		},
		"the description links no task": {
			edit: func(p *pieces, _ *Reviser) { p.reviews.hasTask = false },
			want: "closes no issue in this repository",
		},
		"there was nothing left to answer": {
			edit: func(p *pieces, _ *Reviser) { p.reviews.items = nil },
			want: "nothing here loomlc hadn't already answered",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			r, p := reviser(t, tt.edit)

			rev, err := r.Revise(context.Background(), sink.Ref{})
			if err != nil {
				t.Fatalf("Revise: %v", err)
			}

			if rev.Pushed {
				t.Error("something was pushed")
			}
			if p.pusher.branch != "" {
				t.Errorf("pushed %q, want nothing", p.pusher.branch)
			}
			if !rev.Replied {
				t.Fatal("the reviewer got no answer")
			}
			if len(p.reviews.comments) != 1 || !strings.Contains(p.reviews.comments[0], tt.want) {
				t.Errorf("reply = %q, want it to say %q", p.reviews.comments, tt.want)
			}
			if len(p.reviews.released) != 1 {
				t.Errorf("released %v, want the labels cleared once", p.reviews.released)
			}
		})
	}
}

// A pull request loomlc has no business revising is left exactly as it is: no claim, no comment, and no
// label touched, since the labels on a stranger's pull request aren't loomlc's to clear.
func TestReviseLeavesAPullRequestItCannotReviseAlone(t *testing.T) {
	tests := map[string]struct {
		out  sink.Output
		want string
	}{
		"closed": {
			out:  sink.Output{Ref: sink.Ref{Sink: "github-pr", ID: "44"}, Branch: "feat/x", Base: "main"},
			want: "is closed",
		},
		"from a fork": {
			out:  sink.Output{Ref: sink.Ref{Sink: "github-pr", ID: "44"}, Branch: "feat/x", Base: "main", Open: true, CrossRepo: true},
			want: "comes from a fork",
		},
		"no branch": {
			out:  sink.Output{Ref: sink.Ref{Sink: "github-pr", ID: "44"}, Base: "main", Open: true},
			want: "names no branch",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			r, p := reviser(t, func(p *pieces, _ *Reviser) { p.reviews.output = tt.out })

			rev, err := r.Revise(context.Background(), sink.Ref{Sink: "github-pr", ID: "44"})
			if err != nil {
				t.Fatalf("Revise: %v", err)
			}

			if !strings.Contains(rev.Idle, tt.want) {
				t.Errorf("idle = %q, want it to say %q", rev.Idle, tt.want)
			}
			if len(p.reviews.claimed)+len(p.reviews.released)+len(p.reviews.comments) != 0 {
				t.Errorf("claimed %v, released %v, commented %v; want nothing touched",
					p.reviews.claimed, p.reviews.released, p.reviews.comments)
			}
			if len(p.spaces.released) != 0 || p.steps.spec.Dir != "" {
				t.Error("a workspace was prepared for a pull request that can't be revised")
			}
		})
	}
}

func TestReviseSaysWhenNothingIsAskingForChanges(t *testing.T) {
	r, p := reviser(t, func(p *pieces, _ *Reviser) { p.reviews.next = nil })

	rev, err := r.Revise(context.Background(), sink.Ref{})
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}

	if rev.Idle == "" || rev.Claimed {
		t.Errorf("revision = %+v, want it idle and nothing claimed", rev)
	}
	if p.steps.spec.Dir != "" {
		t.Error("the steps ran with nothing to revise")
	}
}

// A killed provider returns the context's error from the step, which is the shape an interrupted
// revision actually takes. Cleanup runs on a context of its own, so the reviewer is still answered and
// the labels and the workspace are still given back.
func TestReviseAnswersAnInterruptedRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, p := reviser(t, func(p *pieces, _ *Reviser) { p.steps.err = context.Canceled })
	cancel()

	rev, err := r.Revise(ctx, sink.Ref{})
	if err == nil {
		t.Fatal("Revise reported no error for a run that was interrupted")
	}

	if !rev.Replied || len(p.reviews.comments) != 1 {
		t.Fatalf("replied = %v, comments = %d; want the reviewer answered", rev.Replied, len(p.reviews.comments))
	}
	reply := p.reviews.comments[0]
	if !strings.Contains(reply, "stopped before it could answer") {
		t.Errorf("the reply doesn't say the revision stopped:\n%s", reply)
	}
	// The error is whatever git, gh or a provider printed. The comment is public; the detail isn't.
	if strings.Contains(reply, context.Canceled.Error()) {
		t.Errorf("the reply quotes the error back to the reviewer:\n%s", reply)
	}
	if !strings.Contains(reply, ".loomlc/runs/") {
		t.Errorf("the reply doesn't say where the record is:\n%s", reply)
	}
	if len(p.reviews.released) != 1 || len(p.spaces.released) != 1 {
		t.Errorf("released labels %v and workspaces %v, want both given back", p.reviews.released, p.spaces.released)
	}
}

// Every way a revision can break leaves the reviewer with an answer: a request cleared with nothing
// said is a reviewer's words thrown away, and under a watch nobody would ever know.
func TestEveryBrokenRevisionStillAnswersTheReview(t *testing.T) {
	broken := errors.New("gh is down")
	breakages := map[string]func(*pieces, *Reviser){
		"the feedback couldn't be read":  func(p *pieces, _ *Reviser) { p.reviews.errs["Feedback"] = broken },
		"the task couldn't be looked up": func(p *pieces, _ *Reviser) { p.reviews.errs["TaskFor"] = broken },
		"the task couldn't be read":      func(p *pieces, _ *Reviser) { p.tasks.getErr = broken },
		"the claim didn't stick":         func(p *pieces, _ *Reviser) { p.reviews.errs["ClaimFeedback"] = broken },
		"the record couldn't be opened":  func(p *pieces, _ *Reviser) { p.log.openErr = broken },
		"the workspace couldn't be made": func(p *pieces, _ *Reviser) { p.spaces.err = broken },
		"a step died":                    func(p *pieces, _ *Reviser) { p.steps.err = broken },
		"the push was rejected":          func(p *pieces, _ *Reviser) { p.pusher.err = broken },
	}
	for name, breaks := range breakages {
		t.Run(name, func(t *testing.T) {
			r, p := reviser(t, breaks)

			rev, err := r.Revise(context.Background(), sink.Ref{})
			if err == nil {
				t.Fatal("Revise reported no error")
			}

			if !rev.Replied || len(p.reviews.comments) != 1 {
				t.Errorf("replied = %v, comments = %q; want the reviewer told the revision stopped", rev.Replied, p.reviews.comments)
			}
			if len(p.reviews.released) != 1 {
				t.Errorf("released %v, want the labels cleared once", p.reviews.released)
			}
			if rev.Answered() {
				t.Error("a revision that broke reports the review as answered")
			}
		})
	}
}

// A reply that can't be posted is the one failure that mustn't pass quietly: the labels come off either
// way, so a swallowed error is a reviewer's request deleted with nothing in its place.
func TestARevisionThatCannotReplyFails(t *testing.T) {
	cases := map[string]func(*pieces, *Reviser){
		"nothing to answer":   func(p *pieces, _ *Reviser) { p.reviews.items = nil },
		"no task to revise":   func(p *pieces, _ *Reviser) { p.reviews.hasTask = false },
		"a finished revision": func(*pieces, *Reviser) {},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			r, p := reviser(t, edit, func(p *pieces, _ *Reviser) {
				p.reviews.errs["CommentOn"] = errors.New("gh is down")
			})

			rev, err := r.Revise(context.Background(), sink.Ref{})
			if err == nil {
				t.Fatal("Revise reported no error when the reviewer couldn't be told anything")
			}
			if rev.Replied || rev.Answered() {
				t.Errorf("revision = %+v, want it to say the review wasn't answered", rev)
			}
			if len(p.reviews.released) != 1 {
				t.Errorf("released %v, want the labels cleared once", p.reviews.released)
			}
		})
	}
}

// A refused push is not an answered review, however much the comment says. A script gating on the exit
// code has to be able to tell them apart.
func TestARefusedPushIsNotAnAnsweredReview(t *testing.T) {
	r, p := reviser(t, func(p *pieces, _ *Reviser) { p.vcs.paths = []string{"loomlc.yml"} })

	rev, err := r.Revise(context.Background(), sink.Ref{})
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}

	if rev.Answered() {
		t.Error("a revision loomlc wouldn't push reports the review as answered")
	}
	if !rev.Replied || len(rev.Refusals) == 0 {
		t.Errorf("revision = %+v, want the reviewer told why it wasn't pushed", rev)
	}
	if p.pusher.branch != "" {
		t.Errorf("pushed %q, want nothing", p.pusher.branch)
	}
}

// A label that stays on hides the pull request from every later pick, so it can't be a line in a log
// that nobody reads.
func TestALabelThatWouldNotComeOffIsAWarning(t *testing.T) {
	r, _ := reviser(t, func(p *pieces, _ *Reviser) {
		p.reviews.errs["ReleaseFeedback"] = errors.New("gh is down")
	})

	rev, err := r.Revise(context.Background(), sink.Ref{})
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}

	if len(rev.Warnings) == 0 {
		t.Fatal("a label that couldn't be cleared left no warning")
	}
	if !strings.Contains(strings.Join(rev.Warnings, "\n"), "before anything offers it again") {
		t.Errorf("warnings = %q, want one saying the pull request won't be offered again", rev.Warnings)
	}
}

// The reply quotes an agent answering a reviewer, so attribution is dropped from the answer itself
// rather than left for the policy to refuse: the policy is the backstop, not the sanitizer.
func TestTheReplyDropsAttributionWithoutLosingTheAnswer(t *testing.T) {
	r, p := reviser(t, func(p *pieces, _ *Reviser) {
		result := revised()
		result.Change.Addressed = []prompt.Addressed{
			{Item: "the twice-read flag", Response: "Read it once.\nCo-authored-by: Claude <noreply@anthropic.com>"},
		}
		p.steps.result = result
	})

	if _, err := r.Revise(context.Background(), sink.Ref{}); err != nil {
		t.Fatalf("Revise: %v", err)
	}

	reply := p.reviews.comments[0]
	if strings.Contains(reply, "Co-authored-by") {
		t.Errorf("the reply carries attribution:\n%s", reply)
	}
	if !strings.Contains(reply, "Read it once.") {
		t.Errorf("the reply lost the answer itself:\n%s", reply)
	}
}

// A fixup's subject is git's "fixup! " in front of whatever the commit it names was called, so on a
// branch with other people's commits it is arbitrary text — and it is quoted in the reply.
func TestTheReplyDropsAttributionFromACommitSubject(t *testing.T) {
	r, p := reviser(t, func(p *pieces, _ *Reviser) {
		result := revised()
		result.Commits = []git.Commit{{SHA: "2222222222222222", Subject: "fixup! Co-authored-by: Claude <noreply@anthropic.com>"}}
		p.steps.result = result
	})

	if _, err := r.Revise(context.Background(), sink.Ref{}); err != nil {
		t.Fatalf("Revise: %v", err)
	}

	if reply := p.reviews.comments[0]; strings.Contains(reply, "Co-authored-by") {
		t.Errorf("the reply carries attribution in a commit subject:\n%s", reply)
	}
}

// An agent's answer is rendered into a bullet, so lines of its own would be bullets nobody wrote.
func TestTheReplyKeepsAnAnswerToOneLine(t *testing.T) {
	r, p := reviser(t, func(p *pieces, _ *Reviser) {
		result := revised()
		result.Change.Addressed = []prompt.Addressed{
			{Item: "the flag", Response: "Fixed.\n- **a comment nobody made** — ignored"},
		}
		p.steps.result = result
	})

	if _, err := r.Revise(context.Background(), sink.Ref{}); err != nil {
		t.Fatalf("Revise: %v", err)
	}

	reply := p.reviews.comments[0]
	bullets := 0
	for _, line := range strings.Split(reply, "\n") {
		if strings.HasPrefix(line, "- **") {
			bullets++
		}
	}
	if bullets != 1 {
		t.Errorf("the reply has %d bullets, want only the one loomlc wrote:\n%s", bullets, reply)
	}
}

// An item sanitized away mustn't promote the response into its place: the reply would show loomlc's own
// answer as the comment it was answering.
func TestAnAnswerToAnAttributedCommentKeepsItsShape(t *testing.T) {
	kept, dropped := sanitizeAddressed([]prompt.Addressed{
		{Item: "Co-authored-by: Claude <noreply@anthropic.com>", Response: "Declined: that is attribution."},
	})

	if len(dropped) == 0 {
		t.Fatal("the attribution survived")
	}
	if len(kept) != 1 || kept[0].Item != "" || kept[0].Response != "Declined: that is attribution." {
		t.Errorf("kept = %+v, want the answer left in the response", kept)
	}
	body := Reply(Revision{Output: sink.Ref{Sink: "github-pr", ID: "44"}, Outcome: lifecycle.Proposed, Passes: 1, Addressed: kept, Pushed: true})
	if !strings.Contains(body, "- **A comment it didn't name** — Declined: that is attribution.") {
		t.Errorf("the reply reads loomlc's answer as the comment:\n%s", body)
	}
}

// A revision that nothing built or tested still pushes when the lifecycle allows it, and the reviewer
// has to be told that — the implement flow says it in the description, and this is the only place a
// revision can say it.
func TestTheReplyPassesOnWhatTheReviewerHasToBeTold(t *testing.T) {
	r, p := reviser(t, func(p *pieces, r *Reviser) {
		r.Publishing.Verified, r.Publishing.AllowUnverified = false, true
		p.checks = &checks{}
	})

	if _, err := r.Revise(context.Background(), sink.Ref{}); err != nil {
		t.Fatalf("Revise: %v", err)
	}

	if reply := p.reviews.comments[0]; !strings.Contains(reply, "nothing built or tested") {
		t.Errorf("the reply doesn't pass on the warning:\n%s", reply)
	}
}

// The reply quotes agents and the reviewers they answer. A reply the policy refuses is cut back to
// loomlc's own words rather than leaving the review unanswered.
func TestAReplyThePolicyRefusesSaysLess(t *testing.T) {
	r, p := reviser(t, func(p *pieces, _ *Reviser) {
		result := revised()
		result.Change.Addressed = []prompt.Addressed{{Item: "the twice-read flag", Response: "Fixed.\nSee https://claude.ai/code/session_1"}}
		p.steps.result = result
		p.policy = text{bans: "claude.ai/code"}
	})

	rev, err := r.Revise(context.Background(), sink.Ref{})
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}

	if !rev.Replied || len(p.reviews.comments) != 1 {
		t.Fatalf("replied = %v, comments = %d; want one reply", rev.Replied, len(p.reviews.comments))
	}
	reply := p.reviews.comments[0]
	if strings.Contains(reply, "claude.ai/code") {
		t.Errorf("the reply carries attribution:\n%s", reply)
	}
	if !strings.Contains(reply, "git rebase -i --autosquash main") {
		t.Errorf("the shorter reply dropped what a person has to do next:\n%s", reply)
	}
}

func TestReviseRefusesAReviserItCannotRun(t *testing.T) {
	r, _ := reviser(t)
	r.Remote = ""

	if _, err := r.Revise(context.Background(), sink.Ref{}); err == nil {
		t.Fatal("Revise ran without a remote to push to")
	}
}
