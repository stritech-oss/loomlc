package run

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/stritech-oss/loomlc/internal/commit"

	"github.com/stritech-oss/loomlc/internal/executor"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/lifecycle"
	"github.com/stritech-oss/loomlc/internal/prompt"
	"github.com/stritech-oss/loomlc/internal/runlog"
	"github.com/stritech-oss/loomlc/internal/sink"
	"github.com/stritech-oss/loomlc/internal/task"
)

// Reviews is what a feedback run needs from the sink the change was proposed to.
type Reviews interface {
	NextFeedback(ctx context.Context, exclude []sink.Ref) (sink.Ref, bool, error)
	Output(ctx context.Context, ref sink.Ref) (sink.Output, error)
	Feedback(ctx context.Context, ref sink.Ref) ([]sink.FeedbackItem, error)
	TaskFor(ctx context.Context, ref sink.Ref) (task.Ref, bool, error)
	ClaimFeedback(ctx context.Context, ref sink.Ref) error
	ReleaseFeedback(ctx context.Context, ref sink.Ref) error
	CommentOn(ctx context.Context, ref sink.Ref, body string) error
}

// Lookup reads the task a change was built for, so a revision can be judged against it.
type Lookup interface {
	Get(ctx context.Context, ref task.Ref) (task.Task, error)
}

// Revisions runs a change through the lifecycle's steps again, against review feedback.
type Revisions interface {
	Revise(ctx context.Context, spec lifecycle.Spec) (lifecycle.Result, error)
}

// Reviser answers a review on one open pull request: it revises the change, pushes the result, and says
// on the pull request what it did about each comment.
//
// It never rewrites what the reviewer has already read. A fix to a commit that is already pushed becomes
// a fixup commit, which stays on the branch until a person folds it in.
type Reviser struct {
	// Lifecycle is the steps and rules the revision runs through. Revise fills in the task, workspace,
	// feedback and log.
	Lifecycle lifecycle.Spec
	// Publishing is what pushing the result may do. Revise fills in the task and workspace.
	Publishing Options
	// Remote is the git remote the pull request's branch lives on.
	Remote string

	Reviews    Reviews
	Tasks      Lookup
	Workspaces Workspaces
	Steps      Revisions
	Publisher  *Publisher
	Push       Pusher
	// Policy checks what loomlc posts: the commit policy holds a comment to the same rules as a commit.
	Policy Text

	// NewLog opens the record for a run id.
	NewLog func(id string) (Log, error)
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Progress receives a line as the revision goes, for an operator watching. Nil is silent.
	Progress io.Writer
}

// Revision is how a feedback run ended, for an operator's terminal, a script's exit code, and the
// comment the reviewer reads.
type Revision struct {
	// Output is the pull request that was reviewed.
	Output sink.Ref
	URL    string
	// Task is the task the change was built for.
	Task task.Ref
	// Base is the branch the pull request merges into, which is what a fold rebases onto.
	Base string
	// Idle says why there was nothing to revise.
	Idle string
	// Outcome is how the lifecycle ended, and Passes how many times the change step ran.
	Outcome lifecycle.Outcome
	Passes  int
	// Addressed is what the change step says it did about each comment, in its own words.
	Addressed []prompt.Addressed
	// Commits are the commits the revision added, oldest first, and Fixups how many of them are fixes a
	// person still has to fold in.
	Commits []git.Commit
	Fixups  int
	// Pushed says the commits reached the pull request's branch.
	Pushed bool
	// Blockers say why the revision answered nothing; Refusals why what it made wasn't pushed.
	Blockers, Refusals []string
	// Warnings are what didn't work but didn't stop the revision.
	Warnings []string
	// Claimed says loomlc marked the pull request as being revised; Replied that the reviewer was
	// answered on it.
	Claimed bool
	Replied bool
	// Log is the directory holding the run's record.
	Log string
}

// Answered reports whether the review got the answer it asked for: the revision passed review, nothing
// refused what it made, whatever it made reached the branch, and the reviewer was told.
//
// A revision that needed no change on the branch counts: the answer was words, and the words were
// posted. A revision loomlc wouldn't push does not, however much it says in the comment.
func (rev Revision) Answered() bool {
	switch {
	case !rev.Replied, rev.Outcome != lifecycle.Proposed, len(rev.Refusals) > 0:
		return false
	default:
		return rev.Pushed || len(rev.Commits) == 0
	}
}

// Revise answers the review on the pull request want names, or on the next one asking for changes.
//
// An error means the revision itself broke. A revision a step refused, or one loomlc wouldn't push,
// comes back as a Revision saying so, with the reasons already on the pull request.
func (r *Reviser) Revise(ctx context.Context, want sink.Ref) (rev Revision, err error) {
	if err := r.valid(); err != nil {
		return Revision{}, err
	}

	ref, idle, err := r.pick(ctx, want)
	switch {
	case err != nil:
		return Revision{}, err
	case idle != "":
		r.say("%s", idle)
		return Revision{Idle: idle}, nil
	}

	// Reading the pull request comes before any label moves: until this answers, loomlc doesn't know
	// whether the pull request is even one it may revise, so a failure here leaves the request standing
	// for the next attempt.
	out, err := r.Reviews.Output(ctx, ref)
	if err != nil {
		return Revision{Output: ref}, err
	}
	// Nothing is claimed and no label is touched on a pull request loomlc has no business revising: a
	// closed one has had its decision made, and one from a fork is somebody else's branch.
	if reason := unrevisable(out); reason != "" {
		r.say("%s: %s", ref, reason)
		return Revision{Output: ref, URL: out.URL, Idle: ref.String() + " " + reason}, nil
	}

	rev = Revision{Output: ref, URL: out.URL, Base: out.Base}
	// From here on the pull request is one loomlc may revise, so the reviewer gets an answer and both
	// labels come off however this ends. Leaving the labels on would have a watch pick the same pull
	// request up again; clearing them without saying anything would make a reviewer's request vanish.
	// A reviewer asks again by adding the label back.
	defer func() {
		if !rev.Replied {
			r.sayNothingWorked(ctx, &rev, err)
		}
		r.releaseLabels(ctx, &rev)
	}()

	items, err := r.Reviews.Feedback(ctx, ref)
	if err != nil {
		return rev, err
	}
	if len(items) == 0 {
		// The label was added, but every comment was loomlc's own, already answered, or from someone
		// without write access. Saying so is what tells the reviewer why nothing happened.
		rev.Idle = ref.String() + " has no review feedback loomlc hasn't already answered"
		r.say("%s", rev.Idle)
		return rev, r.answer(ctx, &rev)
	}
	r.say("%s: %d comments to answer", ref, len(items))

	t, err := r.subject(ctx, ref, &rev)
	if err != nil || rev.Blockers != nil {
		return rev, err
	}
	if err := r.Reviews.ClaimFeedback(ctx, ref); err != nil {
		return rev, err
	}
	rev.Claimed = true

	err = r.work(ctx, out, t, items, &rev)
	return rev, err
}

// sayNothingWorked answers a review that the revision never got far enough to answer itself, so a
// reviewer whose request is about to be cleared isn't left wondering.
//
// It says that the revision stopped and where the record is, and not what the error was: an error here
// carries whatever git, gh or a provider printed, and this comment is public.
func (r *Reviser) sayNothingWorked(ctx context.Context, rev *Revision, err error) {
	if err == nil {
		return // nothing broke; there was simply nothing to say
	}
	r.say("the review wasn't answered: %v", err)
	stopped := *rev
	stopped.Outcome, stopped.Pushed = 0, false
	stopped.Blockers = []string{"The revision stopped before it could answer. What went wrong is in the run's record and in the operator's terminal, not here."}
	if replyErr := r.reply(ctx, stopped); replyErr != nil {
		r.say("nothing could be said on %s either: %v", rev.Output, replyErr)
		return
	}
	rev.Replied = true
}

// subject reads the task the change was built for. A pull request that closes no task of this source's
// is one loomlc can't revise: the steps judge a revision against the task, and there is none to judge
// against, so the reviewer is told rather than shown a guess.
func (r *Reviser) subject(ctx context.Context, ref sink.Ref, rev *Revision) (task.Task, error) {
	taskRef, ok, err := r.Reviews.TaskFor(ctx, ref)
	if err != nil {
		return task.Task{}, err
	}
	if !ok {
		stuck := fmt.Sprintf("%s closes no issue in this repository, so loomlc can't tell which task to revise it against; add a closing reference such as \"Closes #12\" to the description", ref)
		rev.Blockers = []string{stuck}
		r.say("%s", stuck)
		return task.Task{}, r.answer(ctx, rev)
	}
	rev.Task = taskRef

	t, err := r.Tasks.Get(ctx, taskRef)
	if err != nil {
		return task.Task{}, err
	}
	r.say("%s revises %s: %s", ref, taskRef, t.Title)
	return t, nil
}

// work runs the claimed revision, records what happened, and answers the reviewer.
func (r *Reviser) work(ctx context.Context, out sink.Output, t task.Task, items []sink.FeedbackItem, rev *Revision) (err error) {
	id := r.runID(rev.Output)
	log, err := r.NewLog(id)
	if err != nil {
		return err
	}
	record := runlog.Run{
		ID:        id,
		Lifecycle: r.Lifecycle.Name,
		Flow:      "feedback",
		StartedAt: r.now(),
		Task:      runlog.Task{Ref: t.Ref.String(), Title: t.Title, URL: t.URL},
		Output:    &runlog.Output{Ref: rev.Output.String(), URL: rev.URL},
	}
	rev.Log = log.Dir()
	defer func() {
		record.EndedAt = r.now()
		if err != nil {
			record.Error = err.Error()
		}
		if saveErr := log.Save(record); saveErr != nil {
			r.say("the run's record couldn't be written: %v", saveErr)
		}
	}()

	err = r.attempt(ctx, out, t, items, log, &record, rev)
	return err
}

// attempt is the revision itself: the pull request's own branch in a workspace, the lifecycle's steps
// against the feedback, and then either a push or the reasons there wasn't one. It fills in the record
// and the revision as it goes, so what it learned survives an error on the next line.
func (r *Reviser) attempt(ctx context.Context, out sink.Output, t task.Task, items []sink.FeedbackItem, log Log, record *runlog.Run, rev *Revision) error {
	ws, err := r.Workspaces.Prepare(ctx, executor.Spec{
		Key:    workspaceKey(t.Ref),
		RunID:  record.ID,
		Branch: out.Branch,
		Base:   out.Base,
		Mode:   executor.ExistingBranch,
	})
	if err != nil {
		return err
	}
	defer r.release(ctx, ws)

	record.Branch, record.Base, record.Backup = ws.Branch, ws.BaseSHA, ws.Backup
	r.say("working in %s on %s", ws.Dir, ws.Branch)
	if ws.Backup != "" {
		r.say("an earlier attempt's commits are saved at %s", ws.Backup)
	}

	spec := r.Lifecycle
	spec.Task, spec.Dir, spec.Branch, spec.Base, spec.Log, spec.Feedback = t, ws.Dir, ws.Branch, ws.BaseSHA, log, items
	result, err := r.Steps.Revise(ctx, spec)
	record.Passes, record.Gates = result.Passes, gateRecords(result.Gates)
	record.Commits = commitRecords(result.Commits)
	rev.Outcome, rev.Passes = result.Outcome, result.Passes
	rev.Addressed, rev.Commits, rev.Fixups = result.Change.Addressed, result.Commits, fixups(result.Commits)
	record.Outcome = result.Outcome.String()
	if err != nil {
		return err
	}

	if result.Outcome != lifecycle.Proposed {
		record.Blockers, rev.Blockers = result.Blockers, result.Blockers
		return r.answer(ctx, rev)
	}
	if len(result.Commits) == 0 {
		// The feedback was answered in words rather than in code. There is nothing to push, and the
		// answers are the reply.
		r.say("the revision changed nothing on the branch")
		return r.answer(ctx, rev)
	}

	opts := r.Publishing
	opts.Task, opts.Dir, opts.Base, opts.Branch, opts.Revising = t.Ref, ws.Dir, ws.BaseSHA, ws.Branch, true
	decision, err := r.Publisher.Prepare(ctx, opts)
	if err != nil {
		return err
	}
	record.Refusals, rev.Refusals, rev.Warnings = decision.Refusals, decision.Refusals, decision.Warnings
	record.Gates = append(record.Gates, gateRecords(decision.Gates)...)
	if !decision.OK() {
		return r.answer(ctx, rev)
	}

	if err := r.Push.Push(ctx, ws.Dir, r.Remote, ws.Branch); err != nil {
		return err
	}
	rev.Pushed = true
	r.say("pushed %d commits to %s", len(rev.Commits), ws.Branch)
	return r.answer(ctx, rev)
}

// pick chooses the pull request to revise: the one the operator named, or the next one asking for
// changes.
func (r *Reviser) pick(ctx context.Context, want sink.Ref) (sink.Ref, string, error) {
	if want.ID != "" {
		return want, "", nil
	}
	ref, ok, err := r.Reviews.NextFeedback(ctx, nil)
	switch {
	case err != nil:
		return sink.Ref{}, "", err
	case !ok:
		return sink.Ref{}, "no pull request is asking for changes", nil
	}
	return ref, "", nil
}

// unrevisable says why a pull request can't be revised, or "" when it can.
func unrevisable(out sink.Output) string {
	switch {
	case !out.Open:
		return "is closed, so there is nothing to revise"
	case out.CrossRepo:
		return "comes from a fork, and loomlc can't push to somebody else's branch"
	case out.Branch == "":
		return "names no branch, so there is nothing to check out"
	case out.Base == "":
		return "names no base branch"
	default:
		return ""
	}
}

// answer says on the pull request what the revision did, which is the whole point of a feedback run: a
// reviewer who gets no reply has to go and read the branch to find out what happened.
func (r *Reviser) answer(ctx context.Context, rev *Revision) error {
	if err := r.reply(ctx, *rev); err != nil {
		return err
	}
	rev.Replied = true
	return nil
}

// reply posts the comment. The text quotes agents and the reviewers they answer, so it goes through the
// same attribution rules as a commit message, and text the policy still refuses is cut back to loomlc's
// own words rather than leaving the review unanswered.
func (r *Reviser) reply(ctx context.Context, rev Revision) error {
	ctx, cancel := cleanup(ctx)
	defer cancel()

	var dropped []string
	rev.Addressed, dropped = sanitizeAddressed(rev.Addressed)
	for _, each := range []struct {
		items *[]string
	}{{&rev.Blockers}, {&rev.Refusals}, {&rev.Warnings}} {
		clean, gone := sanitizeEach(*each.items)
		*each.items, dropped = clean, append(dropped, gone...)
	}
	// A commit subject is quoted too, and a fixup's is git's own prefix in front of whatever the commit
	// it names was called — on a branch with other people's commits, that is any text at all.
	rev.Commits = slices.Clone(rev.Commits)
	for i, c := range rev.Commits {
		clean, gone := sanitizeSubject(c.Subject)
		rev.Commits[i].Subject, dropped = clean, append(dropped, gone...)
	}
	if len(dropped) > 0 {
		r.say("dropped attribution from the reply: %s", strings.Join(dropped, "; "))
	}

	body := Reply(rev)
	if err := r.Policy.CheckText(ctx, body); err != nil {
		r.say("the reply didn't pass the commit policy, so it says less: %v", err)
		body = Reply(Revision{
			Output:  rev.Output,
			Base:    rev.Base,
			Outcome: rev.Outcome,
			Passes:  rev.Passes,
			Fixups:  rev.Fixups,
			Pushed:  rev.Pushed,
			Log:     rev.Log,
		})
		if err := r.Policy.CheckText(ctx, body); err != nil {
			return fmt.Errorf("answer the review on %s: %w", rev.Output, err)
		}
	}
	if err := r.Reviews.CommentOn(ctx, rev.Output, body); err != nil {
		return fmt.Errorf("answer the review on %s: %w", rev.Output, err)
	}
	return nil
}

// releaseLabels clears both feedback labels, so the pull request is no longer asking for changes and no
// longer marked as being revised.
//
// A failure is a warning rather than a line in a log, because the label that is left behind is the one
// that hides a pull request from every later pick: nothing would ever offer it again.
func (r *Reviser) releaseLabels(ctx context.Context, rev *Revision) {
	ctx, cancel := cleanup(ctx)
	defer cancel()
	if err := r.Reviews.ReleaseFeedback(ctx, rev.Output); err != nil {
		warning := fmt.Sprintf("%s still carries its feedback labels, and a person has to clear them before anything offers it again: %v", rev.Output, err)
		rev.Warnings = append(rev.Warnings, warning)
		r.say("%s", warning)
	}
}

// release gives the workspace back. Its branch and commits stay in the repository, so a revision that
// wasn't pushed is still there to look at.
func (r *Reviser) release(ctx context.Context, ws executor.Workspace) {
	ctx, cancel := cleanup(ctx)
	defer cancel()
	if err := r.Workspaces.Release(ctx, ws); err != nil {
		r.say("the workspace at %s is still there: %v", ws.Dir, err)
	}
}

// fixups counts the commits a person still has to fold in.
func fixups(commits []git.Commit) int {
	n := 0
	for _, c := range commits {
		if c.Fixup() {
			n++
		}
	}
	return n
}

// autosquashPrefixes are the headers git writes itself for a commit waiting to be folded in.
var autosquashPrefixes = []string{"fixup! ", "squash! ", "amend! "}

// sanitizeSubject drops agent attribution from a commit subject. git's own autosquash prefix comes off
// first and goes back on afterwards, because the attribution rules are anchored to the start of a line:
// with "fixup! " in front of it, a trailer is invisible both to the sanitizer and to the policy script
// that checks what loomlc posts.
func sanitizeSubject(subject string) (string, []string) {
	for _, prefix := range autosquashPrefixes {
		rest, found := strings.CutPrefix(subject, prefix)
		if !found {
			continue
		}
		clean, dropped := commit.Sanitize(rest)
		return strings.TrimSpace(prefix + strings.TrimSpace(clean)), dropped
	}
	clean, dropped := commit.Sanitize(subject)
	return strings.TrimSpace(clean), dropped
}

// sanitizeAddressed drops agent attribution from each answer. The item and the response are cleaned
// separately rather than as a list, so that an item sanitized down to nothing can't promote the response
// into its place and render loomlc's answer as the comment it was answering. Both are rendered into one
// bullet, and the policy's rules are anchored to the start of a line, so neither can be cleaned as part
// of the finished text.
func sanitizeAddressed(items []prompt.Addressed) ([]prompt.Addressed, []string) {
	var kept []prompt.Addressed
	var dropped []string
	for _, a := range items {
		item, goneFromItem := commit.Sanitize(a.Item)
		response, goneFromResponse := commit.Sanitize(a.Response)
		dropped = append(append(dropped, goneFromItem...), goneFromResponse...)
		item, response = strings.TrimSpace(item), strings.TrimSpace(response)
		if item == "" && response == "" {
			continue
		}
		kept = append(kept, prompt.Addressed{Item: item, Response: response})
	}
	return kept, dropped
}

func (r *Reviser) runID(ref sink.Ref) string {
	return r.now().UTC().Format("20060102-150405.000") + "-" + strings.ToLower(ref.Sink+"-"+ref.ID)
}

func (r *Reviser) now() time.Time { return now(r.Now) }

func (r *Reviser) say(format string, args ...any) { say(r.Progress, format, args...) }

func (r *Reviser) valid() error {
	var missing []string
	for _, part := range []struct {
		name string
		got  bool
	}{
		{"sink", r.Reviews != nil},
		{"source", r.Tasks != nil},
		{"executor", r.Workspaces != nil},
		{"lifecycle runner", r.Steps != nil},
		{"publisher", r.Publisher != nil},
		{"git client", r.Push != nil},
		{"commit policy checker", r.Policy != nil},
		{"run log", r.NewLog != nil},
		{"remote", r.Remote != ""},
	} {
		if !part.got {
			missing = append(missing, part.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("revise %s: the reviser was built without a %s", r.Lifecycle.Name, strings.Join(missing, ", a "))
	}
	return nil
}
