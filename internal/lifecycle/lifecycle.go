// Package lifecycle runs a task through its steps: plan, then a change step and the step that judges it,
// until the judgement passes or the passes run out. A feedback run skips the plan and revises a change
// that was already proposed, against what a reviewer said about it.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/stritech-oss/loomlc/internal/gate"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/prompt"
	"github.com/stritech-oss/loomlc/internal/provider"
	"github.com/stritech-oss/loomlc/internal/sink"
	"github.com/stritech-oss/loomlc/internal/task"
)

// Agent runs one step with a provider's CLI.
type Agent interface {
	Run(ctx context.Context, req provider.Request) (provider.Result, error)
}

// VCS is what the loop needs from git. The engine makes every commit, so an agent never has to.
type VCS interface {
	ChangedPaths(ctx context.Context, dir string) ([]string, error)
	Log(ctx context.Context, dir, base string) ([]git.Commit, error)
	Head(ctx context.Context, dir string) (string, error)
	CommitPaths(ctx context.Context, dir, message string, paths []string) (string, error)
	CommitFixup(ctx context.Context, dir, target, why string, paths []string) (string, error)
	ResetSoft(ctx context.Context, dir, rev string) error
}

// Checks runs the lifecycle's gate commands.
type Checks interface {
	Run(ctx context.Context, dir string, commands [][]string) ([]gate.Result, error)
}

// Recorder keeps what each step was asked and what it answered. It is the only record of what an agent
// was told, so a write that fails stops the run rather than leaving a run nobody can audit.
type Recorder interface {
	Step(name, prompt, answer string) error
}

// Step is one step of the lifecycle, resolved from configuration.
type Step struct {
	Name string
	// Output is what the step answers with: plan, change, or verdict.
	Output string
	// Role is the step's standing instructions, already loaded.
	Role  string
	Agent Agent
	// Model and Vendor are passed to the provider.
	Model, Vendor string
	// Commands the agent may run, as argument vectors.
	Allowed [][]string
	// Gate runs before this step; its results go into the step's prompt.
	Gate [][]string
	// Timeout bounds the step.
	Timeout time.Duration
}

// Spec is one run of a lifecycle over one task.
type Spec struct {
	Name string
	Task task.Task
	// Dir is the workspace, Branch its branch, Base the commit its work starts after.
	Dir, Branch, Base string
	// Plan, Change and Verdict are the three steps, in the order they run.
	Plan, Change, Verdict Step
	// Feedback is the review feedback a Revise run answers. It is required there, and unused by
	// Implement.
	Feedback []sink.FeedbackItem
	// MaxPasses caps the change-and-judge loop.
	MaxPasses int
	// Log records each step of this run. Nil records nothing.
	Log Recorder
	// ProtectedPaths are paths the run must not change.
	ProtectedPaths []string
}

// Outcome is how a run ended.
type Outcome int

const (
	// Proposed means the work passed review and is ready to publish.
	Proposed Outcome = iota + 1
	// Blocked means a step said the work can't be done, so nothing was proposed.
	Blocked
	// Exhausted means the passes ran out before the work passed review.
	Exhausted
)

func (o Outcome) String() string {
	switch o {
	case Proposed:
		return "proposed"
	case Blocked:
		return "blocked"
	case Exhausted:
		return "exhausted"
	default:
		return fmt.Sprintf("outcome %d", int(o))
	}
}

// Result is what a run produced.
type Result struct {
	Outcome Outcome
	Plan    prompt.Plan
	// Change and Verdict are the last of each the run got.
	Change  prompt.Change
	Verdict prompt.Verdict
	// Passes is how many times the change step ran.
	Passes int
	// Commits are the commits the engine made over every pass, oldest first.
	Commits []git.Commit
	// Gates are the results of the last checks that ran.
	Gates []gate.Result
	// Blockers say why, when the outcome is Blocked or Exhausted.
	Blockers []string
}

// Options configure a Runner.
type Options struct {
	// VCS is the workspace the engine commits through.
	VCS VCS
	// Checks runs the gate commands.
	Checks Checks
	// Env is the complete environment an agent's CLI runs with; it inherits nothing else, so a step
	// with no PATH can't start any of the commands it is allowed to run.
	Env []string
	// Progress receives a line per step, for an operator watching. Nil is silent.
	Progress io.Writer
}

// Runner runs lifecycles.
type Runner struct {
	vcs      VCS
	checks   Checks
	env      []string
	progress io.Writer
}

// New returns a Runner.
func New(opts Options) *Runner {
	return &Runner{vcs: opts.VCS, checks: opts.Checks, env: opts.Env, progress: opts.Progress}
}

// Implement takes a task from a plan to work that passed review, or says why it couldn't.
func (r *Runner) Implement(ctx context.Context, spec Spec) (Result, error) {
	if err := spec.valid(prompt.Implement); err != nil {
		return Result{}, err
	}

	plan, gates, err := r.plan(ctx, spec)
	if err != nil {
		return Result{}, err
	}
	if plan.Blocked() {
		r.say("plan: blocked")
		return Result{Outcome: Blocked, Plan: plan, Gates: gates, Blockers: plan.Blockers}, nil
	}
	return r.loop(ctx, spec, prompt.Implement, plan, gates)
}

// Revise answers review feedback on a change that is already proposed. There is no plan step: the
// feedback is what the change step works from, and the step that judged the change judges the revision
// against the same feedback.
//
// Fixes to commits the reviewer has already read become fixup commits, which stay on the branch for them
// to read. Folding those in is a person's job, so Revise never rewrites what was pushed.
func (r *Runner) Revise(ctx context.Context, spec Spec) (Result, error) {
	if err := spec.valid(prompt.Feedback); err != nil {
		return Result{}, err
	}
	return r.loop(ctx, spec, prompt.Feedback, prompt.Plan{}, nil)
}

// loop runs the change step and the step that judges it, until the judgement passes or the passes run
// out. It is the same loop in both modes; what differs is where the change step takes its direction
// from, which is the plan in an implement run and the feedback in a revision.
func (r *Runner) loop(ctx context.Context, spec Spec, mode prompt.Mode, plan prompt.Plan, gates []gate.Result) (Result, error) {
	result := Result{Plan: plan, Gates: gates}
	var findings []prompt.Finding

	for pass := 1; pass <= spec.MaxPasses; pass++ {
		result.Passes = pass

		change, made, problems, err := r.change(ctx, spec, mode, plan, findings, gates, pass)
		if err != nil {
			return result, err
		}
		result.Change = change
		// Every pass's commits, not the last pass's: a run that commits on one pass and answers in words
		// on the next has still put work on the branch, and whatever reads this decides whether to push,
		// what to say was pushed, and how much is left for a person to fold in.
		result.Commits = append(result.Commits, made...)

		if change.Blocked() {
			r.say("%s: blocked on pass %d", spec.Change.Name, pass)
			result.Outcome, result.Blockers = Blocked, change.Blockers
			return result, nil
		}
		if len(problems) > 0 {
			// Nothing was committed, so there's nothing to review: the next pass answers the problems.
			findings, gates = problems, nil
			continue
		}

		gates, err = r.check(ctx, spec.Dir, spec.Verdict.Gate)
		if err != nil {
			return result, err
		}
		result.Gates = gates
		if failed := gate.Failed(gates); len(failed) > 0 {
			r.say("checks failed on pass %d: %s", pass, names(failed))
			findings = gateFindings(failed)
			continue
		}

		verdict, err := r.verdict(ctx, spec, mode, plan, change, gates, pass)
		if err != nil {
			return result, err
		}
		result.Verdict = verdict
		if verdict.Passed() {
			r.say("%s: passed on pass %d", spec.Verdict.Name, pass)
			result.Outcome = Proposed
			return result, nil
		}
		r.say("%s: failed on pass %d with %d findings", spec.Verdict.Name, pass, len(verdict.Findings))
		findings = verdictFindings(verdict)
	}

	result.Outcome = Exhausted
	result.Blockers = exhaustedBlockers(result)
	return result, nil
}

func (r *Runner) say(format string, args ...any) {
	if r.progress == nil {
		return
	}
	_, _ = fmt.Fprintf(r.progress, format+"\n", args...)
}

var errNoAgent = errors.New("the step has no agent to run it")

func (s Spec) valid(mode prompt.Mode) error {
	switch {
	case s.Dir == "" || s.Branch == "" || s.Base == "":
		return fmt.Errorf("run %s: the workspace, branch and base are all required", s.Name)
	case s.MaxPasses < 1:
		return fmt.Errorf("run %s: max passes is %d", s.Name, s.MaxPasses)
	case s.Change.Agent == nil || s.Verdict.Agent == nil:
		return fmt.Errorf("run %s: %w", s.Name, errNoAgent)
	case mode == prompt.Implement && s.Plan.Agent == nil:
		return fmt.Errorf("run %s: %w", s.Name, errNoAgent)
	case mode == prompt.Feedback && len(s.Feedback) == 0:
		// The change step would be told to answer a review and shown nothing to answer.
		return fmt.Errorf("revise %s: there is no feedback to address", s.Name)
	}
	return nil
}
