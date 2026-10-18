package lifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stritech-oss/loomlc/internal/gate"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/sink"
)

// reviewed is the commit a reviewer has already read, which a fix on this branch points at.
var reviewed = git.Commit{SHA: "abc1234def5678", Subject: "feat(cli): print the build commit"}

const (
	changeFixes = `{"status":"complete","summary":"Took the shortcut out.","commits":[{"message":"the flag was read twice","paths":["a.go"],"fixes":"abc1234def5678"}],"addressed":[{"item":"the flag is read twice","response":"read it once"}],"blockers":[]}`
	changeStuck = `{"status":"blocked","summary":"Can't.","commits":[],"addressed":[],"blockers":["the reviewer asks for a rename across a protected path"]}`
)

func feedback() []sink.FeedbackItem {
	return []sink.FeedbackItem{{
		ID:        "RC_1",
		Kind:      "inline",
		Author:    "ada",
		Path:      "a.go",
		Line:      12,
		Body:      "The flag is read twice here.",
		CreatedAt: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC),
	}}
}

// revising returns a spec for a branch that is already proposed and has review feedback on it.
func revising(change, verdict []string) (Spec, *agent, *agent, *agent) {
	s, engineer, reviewer := spec(nil, change, verdict)
	s.Feedback = feedback()
	return s, s.Plan.Agent.(*agent), engineer, reviewer
}

func revise(t *testing.T, s Spec, v *vcs, c *checks) Result {
	t.Helper()
	if c == nil {
		c = &checks{}
	}
	var progress strings.Builder
	result, err := New(Options{VCS: v, Checks: c, Progress: &progress}).Revise(context.Background(), s)
	if err != nil {
		t.Fatalf("Revise: %v\nprogress:\n%s", err, progress.String())
	}
	return result
}

// reviseWithChanges revises a workspace that has more changed files waiting after the first commit, the
// way an agent that edits again on the next pass leaves it.
func reviseWithChanges(t *testing.T, s Spec, v *vcs, next []string) Result {
	t.Helper()
	var progress strings.Builder
	result, err := New(Options{VCS: &refillingVCS{vcs: v, next: next}, Checks: &checks{}, Progress: &progress}).Revise(context.Background(), s)
	if err != nil {
		t.Fatalf("Revise: %v\nprogress:\n%s", err, progress.String())
	}
	return result
}

func TestReviseAnswersTheFeedbackWithAFixup(t *testing.T) {
	s, planner, engineer, reviewer := revising([]string{changeFixes}, []string{verdictPass})
	v := &vcs{changed: []string{"a.go"}, head: "head", log: []git.Commit{reviewed}}

	result := revise(t, s, v, &checks{results: [][]gate.Result{{{Name: "task test", Passed: true}}}})

	if result.Outcome != Proposed || result.Passes != 1 {
		t.Fatalf("result = %+v, want proposed in one pass", result)
	}
	if len(planner.prompts) != 0 {
		t.Error("the plan step ran on a feedback run")
	}
	if len(v.made) != 1 || !strings.HasPrefix(v.made[0], "fixup abc1234def5678: ") {
		t.Errorf("commits made = %q, want a fixup on the commit the reviewer read", v.made)
	}
	for _, step := range []struct {
		name  string
		agent *agent
	}{{"change", engineer}, {"verdict", reviewer}} {
		if len(step.agent.prompts) != 1 {
			t.Fatalf("the %s step ran %d times, want once", step.name, len(step.agent.prompts))
		}
		got := step.agent.prompts[0]
		if !strings.Contains(got, "feedback mode") {
			t.Errorf("the %s step wasn't told it is revising:\n%s", step.name, got)
		}
		if !strings.Contains(got, "The flag is read twice here.") {
			t.Errorf("the %s step wasn't shown the feedback:\n%s", step.name, got)
		}
	}
}

// Nothing the engine does to the branch may rewrite a commit the reviewer has read, so a revision that
// passes still leaves its fixups standing.
func TestReviseLeavesItsFixupsOnTheBranch(t *testing.T) {
	s, _, _, _ := revising([]string{changeFixes}, []string{verdictPass})
	v := &vcs{changed: []string{"a.go"}, head: "head", log: []git.Commit{reviewed}}

	result := revise(t, s, v, &checks{results: [][]gate.Result{{{Name: "task test", Passed: true}}}})

	if len(v.resets) != 0 {
		t.Errorf("reset to %q, want the pushed history left alone", v.resets)
	}
	if len(result.Commits) != 1 || !strings.HasPrefix(result.Commits[0].Subject, "fixup!") {
		t.Errorf("result.Commits = %+v, want the fixup it made", result.Commits)
	}
}

// A pass that commits and a later pass that only answers in words are both part of one revision. If
// only the last pass's commits survived, whatever reads the result would push nothing and tell the
// reviewer the branch didn't change.
func TestReviseKeepsTheCommitsFromEveryPass(t *testing.T) {
	answerOnly := `{"status":"complete","summary":"Explained it instead.","commits":[],"addressed":[{"item":"the test","response":"the existing one covers it"}],"blockers":[]}`
	s, _, _, _ := revising([]string{changeFixes, answerOnly}, []string{verdictFail, verdictPass})
	v := &vcs{changed: []string{"a.go"}, head: "head", log: []git.Commit{reviewed}}

	result := revise(t, s, v, nil)

	if result.Outcome != Proposed || result.Passes != 2 {
		t.Fatalf("result = %+v, want proposed on the second pass", result)
	}
	if len(result.Commits) != 1 || !strings.HasPrefix(result.Commits[0].Subject, "fixup!") {
		t.Errorf("result.Commits = %+v, want the fixup the first pass made", result.Commits)
	}
}

// The same the other way round: a fixup on an early pass and ordinary work on the last one. Both are
// pushed, so both have to be reported — the fixup is what a person still has to fold in.
func TestReviseReportsAFixupFromAnEarlierPass(t *testing.T) {
	more := `{"status":"complete","summary":"Added the test.","commits":[{"message":"test(cli): cover the default","paths":["a_test.go"],"fixes":""}],"addressed":[],"blockers":[]}`
	s, _, _, _ := revising([]string{changeFixes, more}, []string{verdictFail, verdictPass})
	v := &vcs{changed: []string{"a.go"}, head: "head", log: []git.Commit{reviewed}}

	result := reviseWithChanges(t, s, v, []string{"a_test.go"})

	if result.Outcome != Proposed || len(result.Commits) != 2 {
		t.Fatalf("result = %+v, want both passes' commits", result)
	}
	subjects := []string{result.Commits[0].Subject, result.Commits[1].Subject}
	if !strings.HasPrefix(subjects[0], "fixup!") || subjects[1] != "test(cli): cover the default" {
		t.Errorf("commits = %q, want the fixup then the new commit", subjects)
	}
}

func TestReviseRunsOutOfPasses(t *testing.T) {
	s, _, _, _ := revising([]string{changeFixes, changeFixes}, []string{verdictFail, verdictFail})
	s.MaxPasses = 2
	v := &vcs{changed: []string{"a.go"}, head: "head", log: []git.Commit{reviewed}}

	result := revise(t, s, v, nil)

	if result.Outcome != Exhausted || result.Passes != 2 {
		t.Fatalf("result = %+v, want exhausted after two passes", result)
	}
	if len(result.Blockers) == 0 {
		t.Error("an exhausted revision said nothing about why")
	}
}

func TestReviseStopsWhenTheChangeStepIsBlocked(t *testing.T) {
	s, _, _, reviewer := revising([]string{changeStuck}, nil)
	v := &vcs{head: "head", log: []git.Commit{reviewed}}

	result := revise(t, s, v, nil)

	if result.Outcome != Blocked {
		t.Fatalf("outcome = %s, want blocked", result.Outcome)
	}
	if len(result.Blockers) != 1 || !strings.Contains(result.Blockers[0], "protected path") {
		t.Errorf("blockers = %q, want the change step's reason", result.Blockers)
	}
	if len(reviewer.prompts) != 0 {
		t.Error("the review step ran on a blocked revision")
	}
}

// A spec a revision can't run is refused before anything runs: a revision with no feedback would
// otherwise pay for a change step and then ask it to answer a review it was never shown.
func TestReviseRefusesASpecItCannotRun(t *testing.T) {
	v := &vcs{head: "head"}
	// The wanted text is the run's own refusal, not a later complaint from whatever the run would have
	// reached: these say what an operator has to fix in the lifecycle.
	cases := map[string]struct {
		break_ func(*Spec)
		want   string
	}{
		"no feedback":       {func(s *Spec) { s.Feedback = nil }, "revise sdlc: there is no feedback to address"},
		"no change agent":   {func(s *Spec) { s.Change.Agent = nil }, "run sdlc: the step has no agent"},
		"no verdict agent":  {func(s *Spec) { s.Verdict.Agent = nil }, "run sdlc: the step has no agent"},
		"no workspace":      {func(s *Spec) { s.Dir = "" }, "the workspace, branch and base are all required"},
		"no passes allowed": {func(s *Spec) { s.MaxPasses = 0 }, "max passes is 0"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, engineer, reviewer := revising([]string{changeFixes}, []string{verdictPass})
			c.break_(&s)

			_, err := New(Options{VCS: v, Checks: &checks{}}).Revise(context.Background(), s)
			if err == nil {
				t.Fatal("Revise accepted it")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want it to say %q", err, c.want)
			}
			if len(engineer.prompts)+len(reviewer.prompts) != 0 {
				t.Error("a step ran before the spec was refused")
			}
		})
	}
}

// The plan step is the one thing a revision doesn't need, so a lifecycle missing it still revises.
func TestReviseDoesNotNeedAPlanStep(t *testing.T) {
	s, _, _, _ := revising([]string{changeFixes}, []string{verdictPass})
	s.Plan = Step{}
	v := &vcs{changed: []string{"a.go"}, head: "head", log: []git.Commit{reviewed}}

	if result := revise(t, s, v, nil); result.Outcome != Proposed {
		t.Fatalf("outcome = %s, want proposed", result.Outcome)
	}
}

// A feedback run is the other way a step reaches a provider, so it needs the environment too.
func TestARevisionRunsItsStepsWithTheEnvironmentItWasGiven(t *testing.T) {
	s, _, engineer, reviewer := revising([]string{changeFixes}, []string{verdictPass})
	v := &vcs{changed: []string{"a.go"}, head: "head", log: []git.Commit{reviewed}}

	runner := New(Options{VCS: v, Checks: &checks{}, Env: operatorEnv})
	if _, err := runner.Revise(context.Background(), s); err != nil {
		t.Fatalf("Revise: %v", err)
	}

	checkEnv(t, map[string]*agent{"engineer": engineer, "qa": reviewer})
}
