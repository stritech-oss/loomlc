package run

import (
	"context"
	"errors"
	"fmt"

	"github.com/stritech-oss/loomlc/internal/lifecycle"
	"github.com/stritech-oss/loomlc/internal/sink"
	"github.com/stritech-oss/loomlc/internal/task"
)

// Pusher sends the run's branch to the remote.
type Pusher interface {
	Push(ctx context.Context, dir, remote, branch string) error
}

// Outputs is what publishing needs from the sink.
type Outputs interface {
	Propose(ctx context.Context, change sink.Change) (sink.Output, error)
	CommentOn(ctx context.Context, ref sink.Ref, body string) error
}

// Tasks is what publishing needs from the source.
type Tasks interface {
	Comment(ctx context.Context, ref task.Ref, body string) error
	Transition(ctx context.Context, ref task.Ref, status task.Status) error
}

// Text checks a pull request description against the commit policy, which bans agent attribution there
// as well as in commits.
type Text interface {
	CheckText(ctx context.Context, body string) error
}

// Output describes the pull request to open.
type Output struct {
	// Remote is the git remote to push to.
	Remote string
	// Base is the branch the pull request merges into.
	Base string
	// Labels mark the pull request as loomlc's.
	Labels []string
	// Reviewer is asked for a review, best effort.
	Reviewer string
	Draft    bool
	// Template is the pull request template's content, and Sections names the headings to fill.
	Template string
	Sections Sections
	// Steps say which model ran which step, for the summary comment.
	Steps []Step
}

// Publisher gains the publishing half once it has a sink to publish to.
type Opener struct {
	push    Pusher
	outputs Outputs
	tasks   Tasks
	policy  Text
}

// NewOpener returns an Opener.
func NewOpener(push Pusher, outputs Outputs, tasks Tasks, policy Text) *Opener {
	return &Opener{push: push, outputs: outputs, tasks: tasks, policy: policy}
}

// Open publishes work a Decision allowed: it pushes the branch, opens or reuses the pull request, says on
// the task where the work went, and moves the task to done.
//
// The order matters. Nothing is said anywhere until the branch is pushed and the pull request exists, so
// a task is never marked done against work nobody can see. What follows the pull request is best effort:
// a comment that fails doesn't unmake it.
func (o *Opener) Open(ctx context.Context, opts Options, out Output, decision Decision, result lifecycle.Result) (sink.Output, error) {
	if !decision.OK() {
		return sink.Output{}, fmt.Errorf("publish %s: the work wasn't cleared for publishing", opts.Branch)
	}
	if result.Outcome != lifecycle.Proposed {
		return sink.Output{}, fmt.Errorf("publish %s: the run ended %s, so there is nothing to propose", opts.Branch, result.Outcome)
	}

	body, err := Body(out.Template, out.Sections, Parts{
		Description: result.Change.Summary,
		QA:          result.Verdict.Summary,
		Issue:       closes(opts.Task),
		Warnings:    decision.Warnings,
	})
	if err != nil {
		return sink.Output{}, fmt.Errorf("publish %s: %w", opts.Branch, err)
	}
	// The policy bans agent attribution in a description as well as a commit, and a refusal after the
	// push would leave a branch with no pull request.
	if err := o.policy.CheckText(ctx, body); err != nil {
		return sink.Output{}, fmt.Errorf("publish %s: the description doesn't pass the commit policy: %w", opts.Branch, err)
	}

	if err := o.push.Push(ctx, opts.Dir, out.Remote, opts.Branch); err != nil {
		return sink.Output{}, fmt.Errorf("publish %s: %w", opts.Branch, err)
	}
	output, err := o.outputs.Propose(ctx, sink.Change{
		Branch:   opts.Branch,
		Base:     out.Base,
		Title:    result.Plan.PRTitle,
		Body:     body,
		Labels:   out.Labels,
		Reviewer: out.Reviewer,
		Draft:    out.Draft,
	})
	if err != nil {
		return sink.Output{}, fmt.Errorf("publish %s: %w", opts.Branch, err)
	}

	// Everything from here on is bookkeeping about work that already exists.
	var problems []error
	if err := o.outputs.CommentOn(ctx, output.Ref, sink.Marker+"summary -->\n"+Summary(result, out.Steps)); err != nil {
		problems = append(problems, fmt.Errorf("post the summary: %w", err))
	}
	if err := o.tasks.Comment(ctx, opts.Task, sink.Marker+"link -->\nProposed in "+output.URL); err != nil {
		problems = append(problems, fmt.Errorf("say where the work went: %w", err))
	}
	if err := o.tasks.Transition(ctx, opts.Task, task.Done); err != nil {
		problems = append(problems, fmt.Errorf("move %s to done: %w", opts.Task, err))
	}
	return output, errors.Join(problems...)
}

// closes links the pull request to its task, so merging closes it. A source whose tasks aren't GitHub
// issues gets a plain reference, since a closing keyword would mean nothing.
func closes(ref task.Ref) string {
	if ref.ID == "" {
		return ""
	}
	if isNumber(ref.ID) {
		return "Closes #" + ref.ID
	}
	return "For " + ref.String()
}

func isNumber(id string) bool {
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return id != ""
}
