package lifecycle

import (
	"context"
	"fmt"
	"strings"

	"github.com/stritech-oss/loomlc/internal/commit"
	"github.com/stritech-oss/loomlc/internal/gate"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/prompt"
	"github.com/stritech-oss/loomlc/internal/provider"
)

// plan runs the planning step. Its own checks run first: planning against a tree that already fails is
// guesswork, so a failure there blocks the run instead.
func (r *Runner) plan(ctx context.Context, spec Spec) (prompt.Plan, []gate.Result, error) {
	gates, err := r.check(ctx, spec.Dir, spec.Plan.Gate)
	if err != nil {
		return prompt.Plan{}, nil, err
	}
	if failed := gate.Failed(gates); len(failed) > 0 {
		r.say("checks failed before planning: %s", names(failed))
		return prompt.Plan{Status: "blocked", Blockers: gateBlockers(failed)}, gates, nil
	}

	data, err := r.data(ctx, spec, spec.Plan, 1)
	if err != nil {
		return prompt.Plan{}, gates, err
	}
	data.Gates = promptGates(gates)

	var plan prompt.Plan
	if err := r.step(ctx, spec.Plan, prompt.Implement, data, provider.ReadOnly, &plan); err != nil {
		return prompt.Plan{}, gates, err
	}
	r.say("%s: %s", spec.Plan.Name, plan.Status)
	return plan, gates, nil
}

// change runs the change step and makes the commits it asked for. It returns the commits the engine made,
// which is empty when the answer couldn't be acted on.
func (r *Runner) change(ctx context.Context, spec Spec, plan prompt.Plan, findings []prompt.Finding, gates []gate.Result, pass int) (prompt.Change, []git.Commit, []prompt.Finding, error) {
	head, err := r.vcs.Head(ctx, spec.Dir)
	if err != nil {
		return prompt.Change{}, nil, nil, err
	}

	// A change step with checks of its own runs them first; otherwise it reads the ones from the pass
	// before, which is what the prompt calls the checks from the last pass.
	if len(spec.Change.Gate) > 0 {
		gates, err = r.check(ctx, spec.Dir, spec.Change.Gate)
		if err != nil {
			return prompt.Change{}, nil, nil, err
		}
	}

	data, err := r.data(ctx, spec, spec.Change, pass)
	if err != nil {
		return prompt.Change{}, nil, nil, err
	}
	data.Plan = renderPlan(plan)
	data.Findings = findings
	data.Gates = promptGates(gates)

	var change prompt.Change
	if err := r.step(ctx, spec.Change, prompt.Implement, data, provider.WorkspaceWrite, &change); err != nil {
		return prompt.Change{}, nil, nil, err
	}
	r.say("%s: %s, %d commits proposed", spec.Change.Name, change.Status, len(change.Commits))

	// An agent that committed anyway has put its own identity and message in the history. Take the
	// commits back, keeping the work, and make them through the engine like any other.
	if err := r.takeBack(ctx, spec, head); err != nil {
		return change, nil, nil, err
	}
	if change.Blocked() {
		return change, nil, nil, nil
	}
	made, problems, err := r.makeCommits(ctx, spec, change)
	return change, made, problems, err
}

// verdict runs the step that judges the change.
func (r *Runner) verdict(ctx context.Context, spec Spec, plan prompt.Plan, change prompt.Change, gates []gate.Result, pass int) (prompt.Verdict, error) {
	data, err := r.data(ctx, spec, spec.Verdict, pass)
	if err != nil {
		return prompt.Verdict{}, err
	}
	data.Plan = renderPlan(plan)
	data.Change = change.Summary
	data.Gates = promptGates(gates)

	var verdict prompt.Verdict
	if err := r.step(ctx, spec.Verdict, prompt.Implement, data, provider.ReadOnly, &verdict); err != nil {
		return prompt.Verdict{}, err
	}
	return verdict, nil
}

// step renders a step's prompt, runs it, and reads its answer.
func (r *Runner) step(ctx context.Context, step Step, mode prompt.Mode, data prompt.Data, access provider.Access, answer any) error {
	text, err := prompt.Render(mode, step.Output, data)
	if err != nil {
		return err
	}
	schema, err := prompt.Schema(step.Output)
	if err != nil {
		return err
	}
	res, err := step.Agent.Run(ctx, provider.Request{
		Step:            step.Name,
		Dir:             data.Dir,
		RolePrompt:      step.Role,
		Prompt:          text,
		Model:           step.Model,
		Vendor:          step.Vendor,
		Access:          access,
		AllowedCommands: step.Allowed,
		OutputSchema:    schema,
		Timeout:         step.Timeout,
	})
	if err != nil {
		return err
	}
	return prompt.Answer(step.Output, res.Structured, answer)
}

// check runs a step's gate, if it has one.
func (r *Runner) check(ctx context.Context, dir string, commands [][]string) ([]gate.Result, error) {
	if len(commands) == 0 {
		return nil, nil
	}
	return r.checks.Run(ctx, dir, commands)
}

// data fills in what every prompt needs about the run.
func (r *Runner) data(ctx context.Context, spec Spec, step Step, pass int) (prompt.Data, error) {
	commits, err := r.vcs.Log(ctx, spec.Dir, spec.Base)
	if err != nil {
		return prompt.Data{}, err
	}
	return prompt.Data{
		Task:           spec.Task,
		Lifecycle:      spec.Name,
		Step:           step.Name,
		Dir:            spec.Dir,
		Branch:         spec.Branch,
		Base:           spec.Base,
		Iteration:      pass,
		MaxIter:        spec.MaxPasses,
		Commits:        promptCommits(commits),
		Commands:       commands(step.Allowed),
		ProtectedPaths: spec.ProtectedPaths,
	}, nil
}

// takeBack undoes commits an agent made itself, keeping the work in the workspace.
func (r *Runner) takeBack(ctx context.Context, spec Spec, head string) error {
	now, err := r.vcs.Head(ctx, spec.Dir)
	if err != nil {
		return err
	}
	if now == head {
		return nil
	}
	r.say("%s committed on its own; taking the commits back and remaking them", spec.Change.Name)
	return r.vcs.ResetSoft(ctx, spec.Dir, head)
}

// makeCommits makes the commits a change step asked for, once they all check out. Nothing is committed
// when any of them is a problem, so a pass leaves the branch whole or untouched, and the problems come
// back as findings for the next pass to answer.
func (r *Runner) makeCommits(ctx context.Context, spec Spec, change prompt.Change) ([]git.Commit, []prompt.Finding, error) {
	changed, err := r.vcs.ChangedPaths(ctx, spec.Dir)
	if err != nil {
		return nil, nil, err
	}
	existing, err := r.vcs.Log(ctx, spec.Dir, spec.Base)
	if err != nil {
		return nil, nil, err
	}
	if problems := commit.Check(proposed(change), changed, branch(existing)); len(problems) > 0 {
		r.say("the commits %s asked for can't be made: %d problems", spec.Change.Name, len(problems))
		return nil, checkFindings(problems), nil
	}

	var made []git.Commit
	for _, c := range change.Commits {
		message, dropped := commit.Sanitize(c.Message)
		if len(dropped) > 0 {
			r.say("dropped attribution from a commit message: %s", strings.Join(dropped, "; "))
		}
		var sha string
		if c.Fixes != "" {
			sha, err = r.vcs.CommitFixup(ctx, spec.Dir, c.Fixes, message, c.Paths)
		} else {
			sha, err = r.vcs.CommitPaths(ctx, spec.Dir, message, c.Paths)
		}
		if err != nil {
			return made, nil, fmt.Errorf("make the commits %s asked for: %w", spec.Change.Name, err)
		}
		made = append(made, git.Commit{SHA: sha, Subject: subject(message)})
	}
	r.say("made %d commits", len(made))
	return made, nil, nil
}

func subject(message string) string {
	line, _, _ := strings.Cut(message, "\n")
	return line
}
