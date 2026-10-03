package run

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/gate"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/lifecycle"
	"github.com/stritech-oss/loomlc/internal/prompt"
	"github.com/stritech-oss/loomlc/internal/sink"
	"github.com/stritech-oss/loomlc/internal/task"
)

type pusher struct {
	branch string
	err    error
}

func (p *pusher) Push(_ context.Context, _, _, branch string) error {
	p.branch = branch
	return p.err
}

type outputs struct {
	change   sink.Change
	comments []string
	err      error
}

func (o *outputs) Propose(_ context.Context, change sink.Change) (sink.Output, error) {
	o.change = change
	if o.err != nil {
		return sink.Output{}, o.err
	}
	return sink.Output{Ref: sink.Ref{Sink: "github-pr", ID: "45"}, URL: "https://github.com/acme/widgets/pull/45", Open: true}, nil
}

func (o *outputs) CommentOn(_ context.Context, _ sink.Ref, body string) error {
	o.comments = append(o.comments, body)
	return nil
}

type tasks struct {
	comments    []string
	transitions []task.Status
	commentErr  error
}

func (t *tasks) Comment(_ context.Context, _ task.Ref, body string) error {
	t.comments = append(t.comments, body)
	return t.commentErr
}

func (t *tasks) Transition(_ context.Context, _ task.Ref, status task.Status) error {
	t.transitions = append(t.transitions, status)
	return nil
}

type text struct{ err error }

func (t text) CheckText(context.Context, string) error { return t.err }

func passing() lifecycle.Result {
	return lifecycle.Result{
		Outcome: lifecycle.Proposed,
		Plan: prompt.Plan{
			Status: "ready", PRTitle: "feat(cli): print the build commit",
			Summary: "Print the commit the binary was built from.", Deferred: []string{"a --json flag"},
		},
		Change:  prompt.Change{Status: "complete", Summary: "Added the variable and a test."},
		Verdict: prompt.Verdict{Verdict: "pass", Summary: "task check passes."},
		Passes:  2,
		Commits: []git.Commit{{SHA: "1111111", Subject: "feat(cli): print the build commit"}},
		Gates:   []gate.Result{{Name: "task check", Passed: true}},
	}
}

func output(t *testing.T) Output {
	return Output{
		Remote: "origin", Base: "main",
		Labels: []string{"agent-pr"}, Reviewer: "maintainer",
		Template: template(t), Sections: sections(),
		Steps: []Step{{Name: "plan", Provider: "claude", Model: "sonnet"}, {Name: "engineer", Provider: "pi", Model: "gemini-3-pro", Vendor: "google"}},
	}
}

func opened() Options {
	return Options{Task: task.Ref{Source: "github", ID: "9"}, Dir: "/work/issue-9", Base: "abc1234", Branch: "feat/issue-9"}
}

func TestOpenPublishesTheWork(t *testing.T) {
	p, o, tk := &pusher{}, &outputs{}, &tasks{}

	out, err := NewOpener(p, o, tk, text{}).Open(context.Background(), opened(), output(t), Decision{}, passing())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if out.Ref.ID != "45" {
		t.Errorf("output = %+v", out)
	}
	if p.branch != "feat/issue-9" {
		t.Errorf("pushed %q", p.branch)
	}
	if o.change.Title != "feat(cli): print the build commit" || o.change.Base != "main" || o.change.Reviewer != "maintainer" {
		t.Errorf("change = %+v", o.change)
	}
	// The description says what the change did; the plan's framing goes in the summary comment.
	for _, want := range []string{"Added the variable and a test.", "task check passes.", "Closes #9"} {
		if !strings.Contains(o.change.Body, want) {
			t.Errorf("description doesn't contain %q:\n%s", want, o.change.Body)
		}
	}
	if len(o.comments) != 1 || !strings.Contains(o.comments[0], sink.Marker) {
		t.Errorf("pull request comments = %q, want one summary carrying loomlc's marker", o.comments)
	}
	if !strings.Contains(o.comments[0], "2 passes") {
		t.Errorf("the summary doesn't say how the loop went:\n%s", o.comments[0])
	}
	if len(tk.comments) != 1 || !strings.Contains(tk.comments[0], "pull/45") {
		t.Errorf("task comments = %q, want the link", tk.comments)
	}
	if len(tk.transitions) != 1 || tk.transitions[0] != task.Done {
		t.Errorf("transitions = %v, want done", tk.transitions)
	}
}

// Nothing is said anywhere until the work exists, so a task is never marked done against work nobody can
// see.
func TestOpenSaysNothingWhenThePushOrTheProposalFails(t *testing.T) {
	t.Run("the push failed", func(t *testing.T) {
		o, tk := &outputs{}, &tasks{}
		_, err := NewOpener(&pusher{err: errors.New("remote rejected")}, o, tk, text{}).Open(context.Background(), opened(), output(t), Decision{}, passing())
		if err == nil {
			t.Fatal("Open succeeded")
		}
		if o.change.Branch != "" || len(tk.comments) != 0 || len(tk.transitions) != 0 {
			t.Error("something was said about work that was never pushed")
		}
	})
	t.Run("the proposal failed", func(t *testing.T) {
		tk := &tasks{}
		_, err := NewOpener(&pusher{}, &outputs{err: errors.New("gh: not found")}, tk, text{}).Open(context.Background(), opened(), output(t), Decision{}, passing())
		if err == nil {
			t.Fatal("Open succeeded")
		}
		if len(tk.comments) != 0 || len(tk.transitions) != 0 {
			t.Error("the task was updated about a pull request that doesn't exist")
		}
	})
}

// A description with attribution would be rejected by CI after the push, leaving a branch with no pull
// request, so it's checked before anything leaves the machine.
func TestOpenChecksTheDescriptionBeforePushing(t *testing.T) {
	p, o := &pusher{}, &outputs{}

	_, err := NewOpener(p, o, &tasks{}, text{err: errors.New("agent attribution is not allowed")}).Open(context.Background(), opened(), output(t), Decision{}, passing())
	if err == nil || !strings.Contains(err.Error(), "doesn't pass the commit policy") {
		t.Fatalf("Open = %v, want a refusal naming the policy", err)
	}
	if p.branch != "" {
		t.Error("the branch was pushed with a description the policy rejects")
	}
}

// Bookkeeping about work that already exists doesn't unmake it.
func TestOpenReportsBookkeepingFailuresWithoutLosingThePullRequest(t *testing.T) {
	tk := &tasks{commentErr: errors.New("gh: issue locked")}

	out, err := NewOpener(&pusher{}, &outputs{}, tk, text{}).Open(context.Background(), opened(), output(t), Decision{}, passing())
	if err == nil || !strings.Contains(err.Error(), "say where the work went") {
		t.Fatalf("Open = %v, want the comment failure reported", err)
	}
	if out.Ref.ID != "45" {
		t.Errorf("output = %+v, want the pull request that was opened", out)
	}
	if len(tk.transitions) != 1 {
		t.Error("the task wasn't moved to done after a comment failed")
	}
}

func TestOpenRefusesWorkItShouldNotPublish(t *testing.T) {
	tests := []struct {
		name     string
		decision Decision
		result   lifecycle.Result
		want     string
	}{
		{name: "not cleared", decision: Decision{Refusals: []string{"a protected path"}}, result: passing(), want: "wasn't cleared"},
		{name: "the run didn't pass", decision: Decision{}, result: lifecycle.Result{Outcome: lifecycle.Exhausted}, want: "ended exhausted"},
		{name: "the run was blocked", decision: Decision{}, result: lifecycle.Result{Outcome: lifecycle.Blocked}, want: "ended blocked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &pusher{}
			if _, err := NewOpener(p, &outputs{}, &tasks{}, text{}).Open(context.Background(), opened(), output(t), tt.decision, tt.result); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Open = %v, want an error about %q", err, tt.want)
			}
			if p.branch != "" {
				t.Error("something was pushed")
			}
		})
	}
}

// An unverified run says so where a reviewer reads it.
func TestOpenCarriesAWarningIntoTheDescription(t *testing.T) {
	o := &outputs{}
	d := Decision{Warnings: []string{"No checks ran: nothing built or tested this change."}}

	if _, err := NewOpener(&pusher{}, o, &tasks{}, text{}).Open(context.Background(), opened(), output(t), d, passing()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !strings.HasPrefix(o.change.Body, "> **No checks ran") {
		t.Errorf("description starts with:\n%s", o.change.Body)
	}
}

// A task whose id isn't a number can't close an issue, so it gets a reference instead of a keyword.
func TestClosesSuitsTheSource(t *testing.T) {
	if got := closes(task.Ref{Source: "github", ID: "9"}); got != "Closes #9" {
		t.Errorf("closes = %q", got)
	}
	if got := closes(task.Ref{Source: "jira", ID: "PROJ-123"}); got != "For jira#PROJ-123" {
		t.Errorf("closes = %q", got)
	}
	if got := closes(task.Ref{}); got != "" {
		t.Errorf("closes = %q, want nothing", got)
	}
}

func TestSummarySaysWhatRanAndHowItWent(t *testing.T) {
	got := Summary(passing(), output(t).Steps)

	for _, want := range []string{
		"| plan | claude | sonnet |",
		"| engineer | pi | google/gemini-3-pro |",
		"Print the commit the binary was built from.",
		"a --json flag",
		"2 passes, then the review passed.",
		"`task check` — passed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary doesn't contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "session") {
		t.Error("the summary mentions a session; a reviewer can't use one and it links to untrusted text")
	}
}
