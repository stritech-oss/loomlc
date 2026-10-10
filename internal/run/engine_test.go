package run

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stritech-oss/loomlc/internal/executor"
	"github.com/stritech-oss/loomlc/internal/gate"
	"github.com/stritech-oss/loomlc/internal/lifecycle"
	"github.com/stritech-oss/loomlc/internal/runlog"
	"github.com/stritech-oss/loomlc/internal/task"
)

// source is the forge a run takes its task from and reports back to.
type source struct {
	tasks
	ready         []task.Ref
	got           task.Task
	getErr        error
	claimed       []task.Ref
	open          int
	inProgress    int
	transitionErr error
}

func (s *source) NextReady(context.Context, []task.Ref) (task.Ref, bool, error) {
	if len(s.ready) == 0 {
		return task.Ref{}, false, nil
	}
	return s.ready[0], true, nil
}
func (s *source) InProgress(context.Context) (int, error)  { return s.inProgress, nil }
func (s *source) OpenOutputs(context.Context) (int, error) { return s.open, nil }

func (s *source) Get(context.Context, task.Ref) (task.Task, error) {
	return s.got, s.getErr
}

func (s *source) Claim(_ context.Context, ref task.Ref) error {
	s.claimed = append(s.claimed, ref)
	return nil
}

// Transition refuses a cancelled context, the way gh does once the run is interrupted.
func (s *source) Transition(ctx context.Context, ref task.Ref, status task.Status) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.transitionErr != nil {
		return s.transitionErr
	}
	return s.tasks.Transition(ctx, ref, status)
}

// workspaces is the executor: it records the workspace a run asked for and whether it gave it back.
type workspaces struct {
	spec     executor.Spec
	backup   string
	released []string
	err      error
	// pushed is what the remote already has, which loomlc won't rewrite.
	pushed bool
}

func (w *workspaces) Pushed(context.Context, string) (bool, error) { return w.pushed, nil }

func (w *workspaces) Prepare(_ context.Context, spec executor.Spec) (executor.Workspace, error) {
	w.spec = spec
	if w.err != nil {
		return executor.Workspace{}, w.err
	}
	return executor.Workspace{
		Key: spec.Key, Dir: "/work/" + spec.Key, Branch: spec.Branch,
		BaseRef: "origin/" + spec.Base, BaseSHA: "abc1234", Isolation: executor.None, Backup: w.backup,
	}, nil
}

// Release refuses a cancelled context, the way git does once the run is interrupted.
func (w *workspaces) Release(ctx context.Context, ws executor.Workspace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.released = append(w.released, ws.Dir)
	return nil
}

// steps is the lifecycle runner.
type steps struct {
	spec   lifecycle.Spec
	result lifecycle.Result
	err    error
	// onRun runs while the steps do, for modelling a run that is interrupted part way.
	onRun func()
}

func (s *steps) Implement(_ context.Context, spec lifecycle.Spec) (lifecycle.Result, error) {
	s.spec = spec
	if s.onRun != nil {
		s.onRun()
	}
	return s.result, s.err
}

// runLog is the record on disk.
type runLog struct {
	saved []runlog.Run
	dir   string
	// openErr is a record that can't be created, such as a full disk.
	openErr error
}

func (l *runLog) Save(r runlog.Run) error      { l.saved = append(l.saved, r); return nil }
func (l *runLog) Step(_, _, _ string) error    { return nil }
func (l *runLog) Dir() string                  { return l.dir }
func (l *runLog) last(t *testing.T) runlog.Run { t.Helper(); return l.saved[len(l.saved)-1] }
func (l *runLog) opened(id string) (Log, error) {
	if l.openErr != nil {
		return nil, l.openErr
	}
	l.dir = ".loomlc/runs/" + id
	return l, nil
}

// ready is a task a run may start on.
func ready() task.Task {
	return task.Task{
		Ref:   task.Ref{Source: "github", ID: "9"},
		Title: "feat(cli): print the build commit",
		Body:  "It prints dev.",
		URL:   "https://github.com/acme/widgets/issues/9",
		State: task.Open, StateName: "open",
	}
}

// parts are a run's fakes, kept together so a test can read what any of them recorded.
type parts struct {
	source  *source
	spaces  *workspaces
	steps   *steps
	log     *runLog
	vcs     *vcs
	checks  *checks
	outputs *outputs
	pusher  *pusher
	// policy is the commit policy over what loomlc posts. A test can swap in the real checker.
	policy Text
}

// engine builds a run whose task is ready, whose lifecycle proposes work, and whose checks pass.
func engine(t *testing.T, edit ...func(*parts, *Engine)) (*Engine, *parts) {
	t.Helper()
	p := &parts{
		source:  &source{ready: []task.Ref{{Source: "github", ID: "9"}}, got: ready()},
		spaces:  &workspaces{},
		steps:   &steps{result: passing()},
		log:     &runLog{},
		vcs:     work(),
		checks:  &checks{results: []gate.Result{{Name: "task check", Passed: true}}},
		outputs: &outputs{},
		pusher:  &pusher{},
		policy:  text{},
	}
	e := &Engine{
		Lifecycle:  lifecycle.Spec{Name: "sdlc", MaxPasses: 3, ProtectedPaths: []string{"loomlc.yml"}},
		Publishing: Options{ProtectedPaths: []string{"loomlc.yml"}, Gates: [][]string{{"task", "check"}}, Verified: true},
		Output:     output(t),
		Branch: func(id, title string) (string, error) {
			return "feat/issue-" + id + "-" + strings.ReplaceAll(strings.ToLower(title), " ", "-"), nil
		},
		MaxOpenOutputs: 5,
		Now:            func() time.Time { return time.Date(2026, 10, 10, 14, 30, 0, 0, time.UTC) },
	}
	for _, edit := range edit {
		edit(p, e)
	}
	e.Source, e.Workspaces, e.Steps = p.source, p.spaces, p.steps
	e.Publisher = New(p.vcs, p.checks, policy{}, nil)
	e.Opener = NewOpener(p.pusher, p.outputs, p.source, p.policy)
	e.Policy = p.policy
	e.NewLog = p.log.opened
	return e, p
}

func TestRunPublishesWorkThatPassed(t *testing.T) {
	e, p := engine(t)

	report, err := e.Run(context.Background(), task.Ref{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !report.Published || report.Output.URL != "https://github.com/acme/widgets/pull/45" {
		t.Errorf("report = %+v, want the work published", report)
	}
	if len(p.source.claimed) != 1 || p.source.claimed[0].ID != "9" {
		t.Errorf("claimed = %v, want the task it ran", p.source.claimed)
	}
	if p.spaces.spec.Key != "github-9" || p.spaces.spec.Branch != "feat/issue-9-feat(cli):-print-the-build-commit" {
		t.Errorf("workspace = %+v, want one key per task on the task's branch", p.spaces.spec)
	}
	if p.spaces.spec.RunID != "20261010-143000.000-github-9" || p.spaces.spec.Mode != executor.NewBranch {
		t.Errorf("workspace = %+v, want a run id naming the time and the task", p.spaces.spec)
	}
	if len(p.spaces.released) != 1 {
		t.Errorf("released %v, want the workspace given back", p.spaces.released)
	}

	// The steps run in the workspace, against what the executor says the work starts from.
	if p.steps.spec.Dir != "/work/github-9" || p.steps.spec.Base != "abc1234" || p.steps.spec.Task.Title != ready().Title {
		t.Errorf("lifecycle spec = %+v", p.steps.spec)
	}
	if p.steps.spec.Log == nil {
		t.Error("the lifecycle ran with nowhere to record its steps")
	}
	if p.steps.spec.Name != "sdlc" || p.steps.spec.MaxPasses != 3 {
		t.Errorf("the lifecycle's own settings were lost: %+v", p.steps.spec)
	}

	saved := p.log.last(t)
	if saved.Outcome != "proposed" || saved.Output == nil || saved.Output.URL == "" {
		t.Errorf("record = %+v, want the outcome and where the work went", saved)
	}
	if saved.ID != "20261010-143000.000-github-9" || saved.Branch != p.spaces.spec.Branch || saved.Base != "abc1234" {
		t.Errorf("record = %+v, want it to name the run, the branch and the base", saved)
	}
	if saved.EndedAt.IsZero() || len(saved.Commits) != 1 || len(saved.Gates) == 0 {
		t.Errorf("record = %+v, want the commits and checks of the run", saved)
	}
	if report.Log != ".loomlc/runs/20261010-143000.000-github-9" {
		t.Errorf("report.Log = %q, want where the record went", report.Log)
	}
}

func TestRunSaysOnTheTaskWhyNothingWasProposed(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(*parts, *Engine)
		want   []string
		absent string
	}{
		{
			name: "a step said it can't be done",
			edit: func(p *parts, _ *Engine) {
				p.steps.result = lifecycle.Result{Outcome: lifecycle.Blocked, Passes: 1, Blockers: []string{"the version package doesn't exist yet"}}
			},
			want: []string{"couldn't do this as the task describes it", "the version package doesn't exist yet"},
		},
		{
			name: "the review never passed",
			edit: func(p *parts, _ *Engine) {
				p.steps.result = lifecycle.Result{Outcome: lifecycle.Exhausted, Passes: 3, Blockers: []string{"no test covers the default"}}
			},
			want: []string{"didn't pass review in 3 passes", "no test covers the default"},
		},
		{
			name: "publishing was refused",
			edit: func(p *parts, _ *Engine) {
				p.vcs.paths = []string{"loomlc.yml"}
			},
			want:   []string{"wouldn't publish it", "loomlc.yml is protected"},
			absent: "pull/45",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, p := engine(t, tt.edit)

			report, err := e.Run(context.Background(), task.Ref{})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if report.Published {
				t.Error("the work was published")
			}
			if len(p.source.comments) != 1 {
				t.Fatalf("task comments = %q, want one saying why", p.source.comments)
			}
			comment := p.source.comments[0]
			for _, want := range append(tt.want, "<!-- loomlc:", ".loomlc/runs/") {
				if !strings.Contains(comment, want) {
					t.Errorf("the comment doesn't say %q:\n%s", want, comment)
				}
			}
			if tt.absent != "" && strings.Contains(comment, tt.absent) {
				t.Errorf("the comment says %q, and nothing was published:\n%s", tt.absent, comment)
			}
			if got := p.source.transitions; len(got) != 1 || got[0] != task.Failed {
				t.Errorf("transitions = %v, want the task marked failed so a person looks at it", got)
			}
			if len(p.spaces.released) != 1 {
				t.Errorf("released %v, want the workspace given back", p.spaces.released)
			}
			if saved := p.log.last(t); saved.Error != "" {
				t.Errorf("record.Error = %q, want a run that ended rather than one that broke", saved.Error)
			}
		})
	}
}

// A run that broke reached no judgement, so the task goes back in the queue instead of being marked
// failed: a machine that broke isn't a task that was turned down.
func TestRunPutsTheTaskBackWhenItBreaks(t *testing.T) {
	e, p := engine(t, func(p *parts, _ *Engine) {
		p.steps.err = errors.New("claude: exit status 1")
	})

	_, err := e.Run(context.Background(), task.Ref{})
	if err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("error = %v, want the failure to reach the operator", err)
	}
	if got := p.source.transitions; len(got) != 1 || got[0] != task.Ready {
		t.Errorf("transitions = %v, want the claim released", got)
	}
	if len(p.source.comments) != 0 {
		t.Errorf("task comments = %q, want nothing said about a run that broke", p.source.comments)
	}
	if saved := p.log.last(t); !strings.Contains(saved.Error, "exit status 1") {
		t.Errorf("record = %+v, want the error written down", saved)
	}
	if len(p.spaces.released) != 1 {
		t.Errorf("released %v, want the workspace given back", p.spaces.released)
	}
}

// Letting go of a claim and a workspace is what an interrupted run most has to finish, and it can't do
// either on the context that was just cancelled.
func TestAnInterruptedRunStillLetsGo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	e, p := engine(t, func(p *parts, _ *Engine) {
		p.steps.onRun = cancel
		p.steps.err = context.Canceled
	})

	if _, err := e.Run(ctx, task.Ref{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation", err)
	}
	if got := p.source.transitions; len(got) != 1 || got[0] != task.Ready {
		t.Errorf("transitions = %v, want the claim released after the run was interrupted", got)
	}
	if len(p.spaces.released) != 1 {
		t.Errorf("released %v, want the workspace given back after the run was interrupted", p.spaces.released)
	}
}

// A pull request that exists is work the task has to stay pointed at, even when the bookkeeping after it
// didn't finish: releasing the claim here would offer the same work again.
func TestWorkThatWasPublishedKeepsItsClaim(t *testing.T) {
	e, p := engine(t, func(p *parts, _ *Engine) {
		p.source.commentErr = errors.New("gh: could not add comment")
	})

	report, err := e.Run(context.Background(), task.Ref{})
	if err == nil || !strings.Contains(err.Error(), "could not add comment") {
		t.Fatalf("error = %v, want the failed comment reported", err)
	}
	if !report.Published {
		t.Fatalf("report = %+v, want the pull request that exists", report)
	}
	if got := p.source.transitions; len(got) != 1 || got[0] != task.Done {
		t.Errorf("transitions = %v, want the task left done", got)
	}
}

// A claim loomlc can't record is a claim it has to give back: nothing is working on the task, and
// nothing in the queue would offer it again.
func TestAClaimIsReleasedWhenTheRunCannotBeRecorded(t *testing.T) {
	e, p := engine(t, func(p *parts, _ *Engine) {
		p.log.openErr = errors.New("mkdir .loomlc/runs: no space left on device")
	})

	_, err := e.Run(context.Background(), task.Ref{})
	if err == nil || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("error = %v, want the reason the run couldn't start", err)
	}
	if got := p.source.transitions; len(got) != 1 || got[0] != task.Ready {
		t.Errorf("transitions = %v, want the claim released", got)
	}
}

// A task that already heard why a run stopped must not also go back in the queue: the next run would
// repeat the work and say the same thing again.
func TestATaskThatHeardWhyKeepsItsClaim(t *testing.T) {
	e, p := engine(t, func(p *parts, _ *Engine) {
		p.steps.result = lifecycle.Result{Outcome: lifecycle.Blocked, Passes: 1, Blockers: []string{"the version package doesn't exist yet"}}
		p.source.transitionErr = errors.New("gh: label agent-failed not found")
	})

	_, err := e.Run(context.Background(), task.Ref{})
	if err == nil || !strings.Contains(err.Error(), "agent-failed not found") {
		t.Fatalf("error = %v, want the failed label change reported", err)
	}
	if len(p.source.comments) != 1 {
		t.Errorf("task comments = %q, want the one saying why", p.source.comments)
	}
	if got := p.source.transitions; len(got) != 0 {
		t.Errorf("transitions = %v, want no second attempt to move the task", got)
	}
}

// loomlc never force-pushes, so a branch the remote already has is one it can't rewrite. Starting over
// would reset the branch and then fail every push, so the run stops before it touches anything.
func TestRunRefusesABranchTheRemoteAlreadyHas(t *testing.T) {
	e, p := engine(t, func(p *parts, _ *Engine) {
		p.spaces.pushed = true
	})

	report, err := e.Run(context.Background(), task.Ref{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Published || p.spaces.spec.Key != "" {
		t.Errorf("report = %+v, workspace = %+v; want nothing prepared", report, p.spaces.spec)
	}
	if len(p.source.comments) != 1 || !strings.Contains(p.source.comments[0], "never rewrites a pushed branch") {
		t.Fatalf("task comments = %q, want one saying why it can't start", p.source.comments)
	}
	if got := p.source.transitions; len(got) != 1 || got[0] != task.Failed {
		t.Errorf("transitions = %v, want the task marked failed so a person looks at it", got)
	}
}

// A review that couldn't be requested is the operator's to know about: GitHub refuses one on your own
// pull request, so it can't fail the run, but it mustn't vanish either.
func TestRunCarriesTheProposalsOwnWarnings(t *testing.T) {
	e, _ := engine(t, func(p *parts, _ *Engine) {
		p.outputs.warnings = []string{"couldn't request a review from maintainer"}
	})

	report, err := e.Run(context.Background(), task.Ref{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "couldn't request a review") {
		t.Errorf("report.Warnings = %q, want the proposal's own warning", report.Warnings)
	}
}

func TestRunTakesNothingWhenThereIsNothingToTake(t *testing.T) {
	tests := []struct {
		name string
		edit func(*parts, *Engine)
		want string
	}{
		{
			name: "no task is ready",
			edit: func(p *parts, _ *Engine) { p.source.ready = nil },
			want: "nothing is ready",
		},
		{
			name: "too much is already open",
			edit: func(p *parts, _ *Engine) { p.source.open, p.source.inProgress = 4, 1 },
			want: "the cap is 5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, p := engine(t, tt.edit)

			report, err := e.Run(context.Background(), task.Ref{})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !strings.Contains(report.Idle, tt.want) {
				t.Errorf("report.Idle = %q, want it to say %q", report.Idle, tt.want)
			}
			if len(p.source.claimed) != 0 || len(p.log.saved) != 0 {
				t.Errorf("claimed %v and saved %d records, want nothing taken", p.source.claimed, len(p.log.saved))
			}
		})
	}
}

// Naming a task is the operator saying to run it, so the cap that paces unattended pickups doesn't apply.
func TestANamedTaskRunsThroughTheCap(t *testing.T) {
	e, p := engine(t, func(p *parts, _ *Engine) {
		p.source.ready, p.source.open = nil, 99
	})

	report, err := e.Run(context.Background(), task.Ref{Source: "github", ID: "9"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Published || len(p.source.claimed) != 1 {
		t.Errorf("report = %+v, claimed = %v; want the named task run anyway", report, p.source.claimed)
	}
}

func TestRunRefusesATaskThatIsFinished(t *testing.T) {
	e, p := engine(t, func(p *parts, _ *Engine) {
		p.source.got = task.Task{Ref: task.Ref{Source: "github", ID: "9"}, State: task.Finished, StateName: "closed as not planned"}
	})

	_, err := e.Run(context.Background(), task.Ref{Source: "github", ID: "9"})
	if err == nil || !strings.Contains(err.Error(), "closed as not planned") {
		t.Fatalf("error = %v, want it to name the source's own state", err)
	}
	if len(p.source.claimed) != 0 {
		t.Errorf("claimed %v, want a finished task left alone", p.source.claimed)
	}
}

// A blocker is an agent's words, and the commit policy bans agent attribution in a comment as much as in
// a commit. The trailer goes; the rest of what the agent said still reaches the task. The real checker
// judges it, because its rules are anchored to the start of a line and a fake's aren't: a trailer inside
// the list loomlc renders is exactly what an anchored rule can't see.
func TestTheCommentDropsAttributionAnAgentWrote(t *testing.T) {
	e, p := engine(t, func(p *parts, _ *Engine) {
		p.steps.result = lifecycle.Result{
			Outcome: lifecycle.Blocked,
			Passes:  1,
			Blockers: []string{
				"the version package doesn't exist yet",
				"Co-authored-by: Claude <noreply@anthropic.com>",
				"see https://claude.ai/code/abc for the session",
			},
		}
		p.policy = policyText{t}
	})

	if _, err := e.Run(context.Background(), task.Ref{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.source.comments) != 1 {
		t.Fatalf("task comments = %q, want one", p.source.comments)
	}
	comment := p.source.comments[0]
	for _, gone := range []string{"Co-authored-by", "claude.ai/code"} {
		if strings.Contains(comment, gone) {
			t.Errorf("the comment carries %q:\n%s", gone, comment)
		}
	}
	if !strings.Contains(comment, "the version package doesn't exist yet") {
		t.Errorf("the comment lost what the agent actually said:\n%s", comment)
	}
}

// Whatever the policy refuses that the sanitizer can't remove, the task hears loomlc's own account of
// the run rather than nothing. The fake stands in for a rule the script might grow; no current rule
// survives the sanitizer.
func TestTheCommentFallsBackToLoomlcsOwnWords(t *testing.T) {
	e, p := engine(t, func(p *parts, _ *Engine) {
		p.steps.result = lifecycle.Result{Outcome: lifecycle.Blocked, Passes: 1, Blockers: []string{"a blocker the policy objects to"}}
		p.policy = text{bans: "objects to"}
	})

	if _, err := e.Run(context.Background(), task.Ref{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.source.comments) != 1 {
		t.Fatalf("task comments = %q, want one", p.source.comments)
	}
	comment := p.source.comments[0]
	if strings.Contains(comment, "objects to") {
		t.Errorf("the comment carries text the policy refused:\n%s", comment)
	}
	for _, want := range []string{"stopped without proposing anything", ".loomlc/runs/"} {
		if !strings.Contains(comment, want) {
			t.Errorf("the fallback comment doesn't say %q:\n%s", want, comment)
		}
	}
}

func TestRunSaysWhereAnEarlierAttemptWentWhenABranchIsReset(t *testing.T) {
	e, p := engine(t, func(p *parts, _ *Engine) {
		p.spaces.backup = "refs/loomlc/attempts/feat/issue-9/20261010-143000.000-github-9"
	})

	report, err := e.Run(context.Background(), task.Ref{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Backup != p.spaces.backup || p.log.last(t).Backup != p.spaces.backup {
		t.Errorf("report = %+v, record = %+v; want the attempt ref written down", report, p.log.last(t))
	}
}

func TestAnEngineBuiltWithoutItsPartsSaysWhichOnesAreMissing(t *testing.T) {
	e := &Engine{Lifecycle: lifecycle.Spec{Name: "sdlc"}}

	_, err := e.Run(context.Background(), task.Ref{})
	if err == nil {
		t.Fatal("Run with no parts succeeded")
	}
	for _, want := range []string{"sdlc", "source", "executor", "run log"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
}

func TestFailureSaysWhatStoppedTheRun(t *testing.T) {
	body := Failure(Problem{
		Outcome:  lifecycle.Proposed,
		Passes:   2,
		Refusals: []string{"the publish gate `task check` failed:\nFAIL\tinternal/cli\ttyped nonsense"},
		Log:      ".loomlc/runs/20261010-143000.000-github-9",
	})

	if !strings.HasPrefix(body, "<!-- loomlc:") {
		t.Errorf("the comment isn't marked as loomlc's own:\n%s", body)
	}
	// Gate output arrives as several lines and belongs to the item above it.
	if !strings.Contains(body, "- the publish gate `task check` failed:\n  FAIL") {
		t.Errorf("the gate's output isn't kept with its refusal:\n%s", body)
	}
	if !strings.Contains(body, ".loomlc/runs/20261010-143000.000-github-9") {
		t.Errorf("the comment doesn't say where to read the rest:\n%s", body)
	}
}

func TestFailureCountsOnePass(t *testing.T) {
	body := Failure(Problem{Outcome: lifecycle.Exhausted, Passes: 1})
	if !strings.Contains(body, "in 1 pass.") {
		t.Errorf("the comment doesn't count one pass as one:\n%s", body)
	}
}
