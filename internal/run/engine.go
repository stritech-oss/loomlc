package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/stritech-oss/loomlc/internal/commit"
	"github.com/stritech-oss/loomlc/internal/executor"
	"github.com/stritech-oss/loomlc/internal/gate"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/lifecycle"
	"github.com/stritech-oss/loomlc/internal/runlog"
	"github.com/stritech-oss/loomlc/internal/sink"
	"github.com/stritech-oss/loomlc/internal/task"
)

// cleanupGrace bounds the work a run does after it was interrupted — releasing its claim and its
// workspace — which it can't do on the context that was just cancelled.
const cleanupGrace = time.Minute

// Source is what a run needs from the source its task came from.
type Source interface {
	NextReady(ctx context.Context, exclude []task.Ref) (task.Ref, bool, error)
	InProgress(ctx context.Context) (int, error)
	OpenOutputs(ctx context.Context) (int, error)
	Get(ctx context.Context, ref task.Ref) (task.Task, error)
	Claim(ctx context.Context, ref task.Ref) error
	Comment(ctx context.Context, ref task.Ref, body string) error
	Transition(ctx context.Context, ref task.Ref, status task.Status) error
}

// Workspaces provides the workspace a run works in, and answers what the remote already has.
type Workspaces interface {
	Prepare(ctx context.Context, spec executor.Spec) (executor.Workspace, error)
	Release(ctx context.Context, ws executor.Workspace) error
	Pushed(ctx context.Context, branch string) (bool, error)
}

// Steps runs a task through the lifecycle's steps.
type Steps interface {
	Implement(ctx context.Context, spec lifecycle.Spec) (lifecycle.Result, error)
}

// Log is a run's record on disk, which internal/runlog writes.
type Log interface {
	Save(r runlog.Run) error
	Step(name, prompt, answer string) error
	Dir() string
}

// Engine takes one task from the queue to a published pull request, and says why on the task when it
// can't. It is one run: the scheduler that runs several is Phase 0's next step.
type Engine struct {
	// Lifecycle is the steps and rules a task runs through. Run fills in the task, workspace and log.
	Lifecycle lifecycle.Spec
	// Publishing is what publishing the result may do. Run fills in the task and workspace.
	Publishing Options
	// Output describes the pull request to open.
	Output Output

	Source     Source
	Workspaces Workspaces
	Steps      Steps
	Publisher  *Publisher
	Opener     *Opener
	// Policy checks what loomlc posts: the commit policy holds a comment to the same rules as a commit.
	Policy Text

	// Branch names a task's branch, such as feat/issue-9-print-the-build-commit.
	Branch func(id, title string) (string, error)
	// MaxOpenOutputs stops picking up work while that many outputs are open or tasks claimed. Zero never
	// stops. A task named outright ignores it.
	MaxOpenOutputs int
	// NewLog opens the record for a run id.
	NewLog func(id string) (Log, error)
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Progress receives a line as the run goes, for an operator watching. Nil is silent.
	Progress io.Writer
}

// Report is how a run ended, for an operator's terminal and a script's exit code.
type Report struct {
	// Task is the task the run took. Its id is empty when no task was taken.
	Task task.Ref
	// Idle says why no task was taken: nothing was ready, or too much is already open.
	Idle string
	// Outcome is how the lifecycle ended.
	Outcome lifecycle.Outcome
	// Output is the pull request, and Published says the work reached it.
	Output    sink.Output
	Published bool
	// Blockers say why the lifecycle proposed nothing; Refusals why what it proposed wasn't published.
	Blockers, Refusals []string
	// Warnings are what the proposal had to tell its reader.
	Warnings []string
	// Claimed says loomlc took the task. A run refused before that left nothing behind.
	Claimed bool
	// Settled says the task's own status already says how the run ended. An unsettled task still carries
	// loomlc's claim, and the run has to give it back.
	Settled bool
	// Log is the directory holding the run's record.
	Log string
	// Backup is the ref holding commits an earlier attempt left on the branch, when there were any.
	Backup string
}

// Run takes the task want names, or the next ready one when want has no id.
//
// An error means the run itself broke, not that the work was rejected: work that was turned down comes
// back as a Report saying so, with the reasons already on the task.
func (e *Engine) Run(ctx context.Context, want task.Ref) (Report, error) {
	if err := e.valid(); err != nil {
		return Report{}, err
	}

	ref, idle, err := e.pick(ctx, want)
	switch {
	case err != nil:
		return Report{}, err
	case idle != "":
		e.say("%s", idle)
		return Report{Idle: idle}, nil
	}

	t, err := e.Source.Get(ctx, ref)
	if err != nil {
		return Report{Task: ref}, err
	}
	if !t.State.Startable() {
		return Report{Task: ref}, fmt.Errorf("%s is %s, so there is nothing to start", ref, stateName(t))
	}
	if err := e.Source.Claim(ctx, ref); err != nil {
		return Report{Task: ref}, err
	}
	e.say("%s: %s", ref, t.Title)

	report, err := e.work(ctx, t)
	report.Claimed = true
	if err != nil && !report.Settled {
		// Nothing was said on the task and nothing was published, so the claim goes back rather than
		// leaving a task marked as being worked on by nobody.
		e.releaseClaim(ctx, ref)
	}
	return report, err
}

// work runs the claimed task, records what happened, and leaves the task's status saying how it ended.
func (e *Engine) work(ctx context.Context, t task.Task) (report Report, err error) {
	id := e.runID(t.Ref)
	log, err := e.NewLog(id)
	if err != nil {
		return Report{Task: t.Ref}, err
	}
	record := runlog.Run{
		ID:        id,
		Lifecycle: e.Lifecycle.Name,
		StartedAt: e.now(),
		Task:      runlog.Task{Ref: t.Ref.String(), Title: t.Title, URL: t.URL},
	}
	report = Report{Task: t.Ref, Log: log.Dir()}
	// The record is written on the way out however the run ended, so an interrupted run still leaves the
	// last thing loomlc knew.
	defer func() {
		record.EndedAt = e.now()
		if err != nil {
			record.Error = err.Error()
		}
		if saveErr := log.Save(record); saveErr != nil {
			e.say("the run's record couldn't be written: %v", saveErr)
		}
	}()

	err = e.attempt(ctx, t, log, &record, &report)
	return report, err
}

// attempt is the run itself: a workspace, the lifecycle's steps, and either a pull request or a comment
// saying why there isn't one. It fills in the record and the report as it goes, so what it learned
// survives an error on the next line.
func (e *Engine) attempt(ctx context.Context, t task.Task, log Log, record *runlog.Run, report *Report) error {
	branch, err := e.Branch(t.Ref.ID, t.Title)
	if err != nil {
		return err
	}
	// loomlc never force-pushes, so a branch the remote already has is one it can't rewrite: an earlier
	// run pushed it and then couldn't open or reuse a pull request for it. Starting over would reset the
	// branch locally and then fail the push on every attempt, so this stops here and asks for a person.
	pushed, err := e.Workspaces.Pushed(ctx, branch)
	if err != nil {
		return err
	}
	if pushed {
		stuck := fmt.Sprintf("the remote already has %s from an earlier run, and loomlc never rewrites a pushed branch; open or revise the pull request for that branch, or delete the branch and run again", branch)
		settled, err := e.reportProblem(ctx, t.Ref, Problem{Log: log.Dir(), Blockers: []string{stuck}})
		record.Blockers, report.Blockers, report.Settled = []string{stuck}, []string{stuck}, settled
		return err
	}

	ws, err := e.Workspaces.Prepare(ctx, executor.Spec{
		Key:    workspaceKey(t.Ref),
		RunID:  record.ID,
		Branch: branch,
		Base:   e.Output.Base,
		Mode:   executor.NewBranch,
	})
	if err != nil {
		return err
	}
	defer e.release(ctx, ws)

	record.Branch, record.Base, record.Backup = ws.Branch, ws.BaseSHA, ws.Backup
	report.Backup = ws.Backup
	e.say("working in %s on %s", ws.Dir, ws.Branch)
	if ws.Backup != "" {
		e.say("an earlier attempt's commits are saved at %s", ws.Backup)
	}

	spec := e.Lifecycle
	spec.Task, spec.Dir, spec.Branch, spec.Base, spec.Log = t, ws.Dir, ws.Branch, ws.BaseSHA, log
	result, err := e.Steps.Implement(ctx, spec)
	record.Passes, record.Commits, record.Gates = result.Passes, commitRecords(result.Commits), gateRecords(result.Gates)
	if err != nil {
		return err
	}
	record.Outcome, report.Outcome = result.Outcome.String(), result.Outcome

	problem := Problem{Outcome: result.Outcome, Passes: result.Passes, Log: log.Dir()}
	if result.Outcome != lifecycle.Proposed {
		record.Blockers, report.Blockers = result.Blockers, result.Blockers
		problem.Blockers = result.Blockers
		settled, err := e.reportProblem(ctx, t.Ref, problem)
		report.Settled = settled
		return err
	}

	opts := e.Publishing
	opts.Task, opts.Dir, opts.Base, opts.Branch = t.Ref, ws.Dir, ws.BaseSHA, ws.Branch
	decision, err := e.Publisher.Prepare(ctx, opts)
	if err != nil {
		return err
	}
	record.Refusals, report.Refusals, report.Warnings = decision.Refusals, decision.Refusals, decision.Warnings
	record.Gates = append(record.Gates, gateRecords(decision.Gates)...)
	if len(decision.Commits) > 0 {
		record.Commits = commitRecords(decision.Commits) // the history as it will be pushed
	}
	if !decision.OK() {
		problem.Refusals = decision.Refusals
		settled, err := e.reportProblem(ctx, t.Ref, problem)
		report.Settled = settled
		return err
	}

	// Open moves the task to done itself, and the pull request can exist even when the bookkeeping after
	// it didn't finish.
	out, err := e.Opener.Open(ctx, opts, e.Output, decision, result)
	if out.Ref.ID != "" {
		// The pull request exists, so the task stays pointed at it even when the bookkeeping after it
		// didn't finish: releasing the claim would offer the same work again.
		report.Output, report.Published, report.Settled = out, true, true
		report.Warnings = append(report.Warnings, out.Warnings...)
		record.Output = &runlog.Output{Ref: out.Ref.String(), URL: out.URL}
		e.say("proposed in %s", out.URL)
	}
	return err
}

// pick chooses the task to run: the one the operator named, or the next ready one. A named task ignores
// the cap and the trigger label — naming it is the operator saying to run it.
func (e *Engine) pick(ctx context.Context, want task.Ref) (task.Ref, string, error) {
	if want.ID != "" {
		return want, "", nil
	}
	if e.MaxOpenOutputs > 0 {
		open, err := e.Source.OpenOutputs(ctx)
		if err != nil {
			return task.Ref{}, "", err
		}
		claimed, err := e.Source.InProgress(ctx)
		if err != nil {
			return task.Ref{}, "", err
		}
		if open+claimed >= e.MaxOpenOutputs {
			return task.Ref{}, fmt.Sprintf("%d outputs are open and %d tasks are claimed, and the cap is %d; name a task to run it anyway", open, claimed, e.MaxOpenOutputs), nil
		}
	}
	ref, ok, err := e.Source.NextReady(ctx, nil)
	switch {
	case err != nil:
		return task.Ref{}, "", err
	case !ok:
		return task.Ref{}, "nothing is ready to pick up", nil
	}
	return ref, "", nil
}

// reportProblem tells the task why nothing was proposed and marks it failed, so a person looks at it
// rather than a watch picking it straight back up. It reports whether the task was told anything at all:
// a task that heard why must not also go back in the queue, or the next run repeats the work and says
// the same thing again.
//
// Saying why is cleanup too, so it runs on a context that outlives an interruption.
func (e *Engine) reportProblem(ctx context.Context, ref task.Ref, p Problem) (bool, error) {
	if reasons := append(slice(p.Blockers), p.Refusals...); len(reasons) > 0 {
		e.say("nothing was proposed: %s", strings.Join(reasons, "; "))
	}
	ctx, cancel := e.cleanup(ctx)
	defer cancel()

	var problems []error
	commented := e.comment(ctx, ref, p)
	if commented != nil {
		problems = append(problems, commented)
	}
	failed := e.Source.Transition(ctx, ref, task.Failed)
	if failed != nil {
		problems = append(problems, fmt.Errorf("mark %s failed: %w", ref, failed))
	}
	return commented == nil || failed == nil, errors.Join(problems...)
}

// comment says on the task why a run proposed nothing. The text quotes agents, so it goes through the
// same attribution rules as a commit message, and text the policy still refuses is cut back to loomlc's
// own words rather than leaving the task with nothing said on it.
func (e *Engine) comment(ctx context.Context, ref task.Ref, p Problem) error {
	var dropped []string
	p.Blockers, dropped = sanitizeEach(p.Blockers)
	refusals, droppedRefusals := sanitizeEach(p.Refusals)
	p.Refusals, dropped = refusals, append(dropped, droppedRefusals...)
	if len(dropped) > 0 {
		e.say("dropped attribution from the comment: %s", strings.Join(dropped, "; "))
	}
	body := Failure(p)
	if err := e.Policy.CheckText(ctx, body); err != nil {
		e.say("the comment didn't pass the commit policy, so it says less: %v", err)
		body = Failure(Problem{Outcome: p.Outcome, Passes: p.Passes, Log: p.Log})
		if err := e.Policy.CheckText(ctx, body); err != nil {
			return fmt.Errorf("say on %s why nothing was proposed: %w", ref, err)
		}
	}
	if err := e.Source.Comment(ctx, ref, body); err != nil {
		return fmt.Errorf("say on %s why nothing was proposed: %w", ref, err)
	}
	return nil
}

// release gives the workspace back. Its branch and commits stay in the repository, so a failed run's
// work is still there for the next attempt.
func (e *Engine) release(ctx context.Context, ws executor.Workspace) {
	ctx, cancel := e.cleanup(ctx)
	defer cancel()
	if err := e.Workspaces.Release(ctx, ws); err != nil {
		e.say("the workspace at %s is still there: %v", ws.Dir, err)
	}
}

// releaseClaim puts the task back in the queue.
func (e *Engine) releaseClaim(ctx context.Context, ref task.Ref) {
	ctx, cancel := e.cleanup(ctx)
	defer cancel()
	if err := e.Source.Transition(ctx, ref, task.Ready); err != nil {
		e.say("%s is still claimed, and a person has to clear the label: %v", ref, err)
		return
	}
	e.say("%s is back in the queue", ref)
}

// cleanup returns a context for the tidying up a run does on its way out, which outlives the
// cancellation that interrupted it: a run that was stopped still has to let go of what it holds.
func (e *Engine) cleanup(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupGrace)
}

// runID names a run after the time it started and the task it took, so the records sort by time and read
// as the work. It becomes a directory name and part of a git ref, so it carries nothing else. The
// milliseconds are there because two runs of one task a second apart would otherwise share a record.
func (e *Engine) runID(ref task.Ref) string {
	return e.now().UTC().Format("20060102-150405.000") + "-" + workspaceKey(ref)
}

// workspaceKey is one key per task, so a second attempt reuses its own workspace rather than a
// stranger's. It is lowercased because that is the charset an executor allows in a key; Phase 0's task
// ids are digits, and a source with case-sensitive ids needs more than this.
func workspaceKey(ref task.Ref) string {
	return strings.ToLower(ref.Source + "-" + ref.ID)
}

func (e *Engine) now() time.Time {
	if e.Now == nil {
		return time.Now()
	}
	return e.Now()
}

func (e *Engine) say(format string, args ...any) {
	if e.Progress == nil {
		return
	}
	_, _ = fmt.Fprintf(e.Progress, format+"\n", args...)
}

func (e *Engine) valid() error {
	var missing []string
	for _, part := range []struct {
		name string
		got  bool
	}{
		{"source", e.Source != nil},
		{"executor", e.Workspaces != nil},
		{"lifecycle runner", e.Steps != nil},
		{"publisher", e.Publisher != nil},
		{"opener", e.Opener != nil},
		{"commit policy checker", e.Policy != nil},
		{"branch template", e.Branch != nil},
		{"run log", e.NewLog != nil},
	} {
		if !part.got {
			missing = append(missing, part.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("run %s: the engine was built without a %s", e.Lifecycle.Name, strings.Join(missing, ", a "))
	}
	return nil
}

// stateName is the source's own word for a task's state, or the engine's when a source gave none.
func stateName(t task.Task) string {
	if t.StateName == "" {
		return t.State.String()
	}
	return t.StateName
}

func commitRecords(commits []git.Commit) []runlog.Commit {
	out := make([]runlog.Commit, 0, len(commits))
	for _, c := range commits {
		out = append(out, runlog.Commit{SHA: c.SHA, Subject: c.Subject})
	}
	return out
}

func gateRecords(results []gate.Result) []runlog.Gate {
	out := make([]runlog.Gate, 0, len(results))
	for _, g := range results {
		out = append(out, runlog.Gate{Name: g.Name, Passed: g.Passed})
	}
	return out
}

// sanitizeEach drops agent attribution from each item of a list, and drops an item left empty. It runs
// per item rather than over the rendered text: the policy's rules are anchored to the start of a line,
// and a trailer survives the "- " that a list puts in front of it.
func sanitizeEach(items []string) ([]string, []string) {
	var kept, dropped []string
	for _, item := range items {
		clean, gone := commit.Sanitize(item)
		dropped = append(dropped, gone...)
		if clean = strings.TrimSpace(clean); clean != "" {
			kept = append(kept, clean)
		}
	}
	return kept, dropped
}

// slice copies a nil-able list so appending to it can't write into its backing array.
func slice(s []string) []string { return append([]string(nil), s...) }
