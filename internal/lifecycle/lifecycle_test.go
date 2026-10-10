package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/gate"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/prompt"
	"github.com/stritech-oss/loomlc/internal/provider"
	"github.com/stritech-oss/loomlc/internal/task"
)

// agent answers with the next scripted answer for its step, and records the prompts it was sent.
type agent struct {
	answers []string
	prompts []string
	err     error
	// onRun runs while the step does, for modelling what an agent does to the workspace.
	onRun func()
}

func (a *agent) Run(_ context.Context, req provider.Request) (provider.Result, error) {
	a.prompts = append(a.prompts, req.Prompt)
	if a.onRun != nil {
		a.onRun()
	}
	if a.err != nil {
		return provider.Result{}, a.err
	}
	if len(a.answers) == 0 {
		return provider.Result{}, fmt.Errorf("no answer scripted for step %s", req.Step)
	}
	answer := a.answers[0]
	a.answers = a.answers[1:]
	return provider.Result{Structured: json.RawMessage(answer)}, nil
}

// vcs is a workspace that records what the engine did to it.
type vcs struct {
	changed  []string
	log      []git.Commit
	head     string
	made     []string // "message" or "fixup <target>: why"
	resets   []string
	failNext error
}

func (v *vcs) ChangedPaths(context.Context, string) ([]string, error) { return v.changed, nil }
func (v *vcs) Log(context.Context, string, string) ([]git.Commit, error) {
	return v.log, nil
}
func (v *vcs) Head(context.Context, string) (string, error) { return v.head, nil }

func (v *vcs) CommitPaths(_ context.Context, _, message string, paths []string) (string, error) {
	if v.failNext != nil {
		err := v.failNext
		v.failNext = nil
		return "", err
	}
	v.made = append(v.made, message)
	sha := fmt.Sprintf("%040d", len(v.made))
	subject, _, _ := strings.Cut(message, "\n")
	v.log = append(v.log, git.Commit{SHA: sha, Subject: subject})
	v.changed = nil
	_ = paths
	return sha, nil
}

func (v *vcs) CommitFixup(_ context.Context, _, target, why string, _ []string) (string, error) {
	v.made = append(v.made, fmt.Sprintf("fixup %s: %s", target, why))
	sha := fmt.Sprintf("%040d", len(v.made))
	v.log = append(v.log, git.Commit{SHA: sha, Subject: "fixup! something"})
	v.changed = nil
	return sha, nil
}

func (v *vcs) ResetSoft(_ context.Context, _, rev string) error {
	v.resets = append(v.resets, rev)
	v.head = rev
	return nil
}

// checks answers with scripted results, one set per pass.
type checks struct {
	results [][]gate.Result
	runs    int
}

func (c *checks) Run(_ context.Context, _ string, commands [][]string) ([]gate.Result, error) {
	if len(commands) == 0 {
		return nil, nil // what the real runner does
	}
	c.runs++
	if len(c.results) == 0 {
		return nil, nil
	}
	out := c.results[0]
	if len(c.results) > 1 {
		c.results = c.results[1:]
	}
	return out, nil
}

const (
	planReady   = `{"status":"ready","pr_title":"feat(cli): print the build commit","summary":"Print it.","files":[{"path":"a.go","change":"add it"}],"tests":["the default"],"deferred":[],"blockers":[]}`
	planBlocked = `{"status":"blocked","pr_title":"feat(cli): print the build commit","summary":"Can't.","files":[],"tests":[],"deferred":[],"blockers":["the version package doesn't exist yet"]}`
	changeDone  = `{"status":"complete","summary":"Added it.","commits":[{"message":"feat(cli): print the build commit","paths":["a.go"],"fixes":""}],"addressed":[],"blockers":[]}`
	verdictPass = `{"verdict":"pass","summary":"Checked it.","findings":[]}`
	verdictFail = `{"verdict":"fail","summary":"Not yet.","findings":[{"detail":"no test covers the default","path":"a.go","line":4}]}`
)

// spec returns a run whose steps answer with the scripted answers.
func spec(plan, change, verdict []string) (Spec, *agent, *agent) {
	planner, engineer, reviewer := &agent{answers: plan}, &agent{answers: change}, &agent{answers: verdict}
	return Spec{
		Name:   "sdlc",
		Task:   task.Task{Ref: task.Ref{Source: "github", ID: "9"}, Title: "Print the build commit", Body: "It prints dev."},
		Dir:    "/work/issue-9",
		Branch: "feat/issue-9",
		Base:   "origin/main",
		Plan: Step{Name: "plan", Output: prompt.OutputPlan, Role: "You plan.", Agent: planner,
			Allowed: [][]string{{"task", "test"}}},
		Change:         Step{Name: "engineer", Output: prompt.OutputChange, Role: "You build.", Agent: engineer},
		Verdict:        Step{Name: "qa", Output: prompt.OutputVerdict, Role: "You review.", Agent: reviewer, Gate: [][]string{{"task", "test"}}},
		MaxPasses:      3,
		ProtectedPaths: []string{"loomlc.yml"},
	}, engineer, reviewer
}

func run(t *testing.T, s Spec, v *vcs, c *checks) Result {
	t.Helper()
	if c == nil {
		c = &checks{}
	}
	var progress strings.Builder
	result, err := New(v, c, &progress).Implement(context.Background(), s)
	if err != nil {
		t.Fatalf("Implement: %v\nprogress:\n%s", err, progress.String())
	}
	return result
}

func TestWorkThatPassesIsProposed(t *testing.T) {
	s, _, reviewer := spec([]string{planReady}, []string{changeDone}, []string{verdictPass})
	v := &vcs{changed: []string{"a.go"}, head: "start"}

	result := run(t, s, v, &checks{results: [][]gate.Result{{{Name: "task test", Passed: true}}}})

	if result.Outcome != Proposed || result.Passes != 1 {
		t.Fatalf("result = %+v, want proposed in one pass", result)
	}
	if len(v.made) != 1 || v.made[0] != "feat(cli): print the build commit" {
		t.Errorf("commits made = %q", v.made)
	}
	if len(result.Commits) != 1 {
		t.Errorf("result.Commits = %+v, want the commit the engine made", result.Commits)
	}
	if len(reviewer.prompts) != 1 || !strings.Contains(reviewer.prompts[0], "Print it.") {
		t.Errorf("the review step wasn't given the plan")
	}
}

func TestABlockedPlanStopsBeforeAnythingIsBuilt(t *testing.T) {
	s, engineer, reviewer := spec([]string{planBlocked}, nil, nil)
	v := &vcs{head: "start"}

	result := run(t, s, v, nil)

	if result.Outcome != Blocked {
		t.Fatalf("outcome = %s, want blocked", result.Outcome)
	}
	if len(result.Blockers) != 1 || !strings.Contains(result.Blockers[0], "doesn't exist yet") {
		t.Errorf("blockers = %q, want the planner's reason", result.Blockers)
	}
	if len(engineer.prompts) != 0 || len(reviewer.prompts) != 0 {
		t.Error("the change and review steps ran on a blocked plan")
	}
	if len(v.made) != 0 {
		t.Errorf("commits made = %q, want none", v.made)
	}
}

func TestFindingsComeBackToTheNextPass(t *testing.T) {
	s, engineer, _ := spec(
		[]string{planReady},
		[]string{changeDone, `{"status":"complete","summary":"Added the test.","commits":[{"message":"test(cli): cover the default","paths":["a_test.go"],"fixes":""}],"addressed":[{"item":"no test","response":"added one"}],"blockers":[]}`},
		[]string{verdictFail, verdictPass},
	)
	v := &vcs{changed: []string{"a.go"}, head: "start"}
	c := &checks{results: [][]gate.Result{{{Name: "task test", Passed: true}}}}

	result := runWithChanges(t, s, v, c, []string{"a_test.go"})

	if result.Outcome != Proposed || result.Passes != 2 {
		t.Fatalf("result = %+v, want proposed on the second pass", result)
	}
	if len(engineer.prompts) != 2 {
		t.Fatalf("the change step ran %d times, want 2", len(engineer.prompts))
	}
	if !strings.Contains(engineer.prompts[1], "no test covers the default") {
		t.Errorf("the second prompt doesn't carry the finding:\n%s", engineer.prompts[1])
	}
	if !strings.Contains(engineer.prompts[1], "a.go:4") {
		t.Errorf("the second prompt doesn't say where the finding is:\n%s", engineer.prompts[1])
	}
}

// runWithChanges refills the workspace's changed paths before each pass, the way an agent editing files
// would.
func runWithChanges(t *testing.T, s Spec, v *vcs, c *checks, next []string) Result {
	t.Helper()
	refill := &refillingVCS{vcs: v, next: next}
	var progress strings.Builder
	result, err := New(refill, c, &progress).Implement(context.Background(), s)
	if err != nil {
		t.Fatalf("Implement: %v\nprogress:\n%s", err, progress.String())
	}
	return result
}

type refillingVCS struct {
	*vcs
	next []string
}

func (r *refillingVCS) ChangedPaths(ctx context.Context, dir string) ([]string, error) {
	if len(r.changed) == 0 {
		r.changed = r.next
	}
	return r.vcs.ChangedPaths(ctx, dir)
}

func TestAFailedCheckSkipsTheReviewAndComesBackAsWork(t *testing.T) {
	secondPass := `{"status":"complete","summary":"Fixed the test.","commits":[{"message":"fix(cli): read the stamped version","paths":["a.go"],"fixes":""}],"addressed":[],"blockers":[]}`
	s, engineer, reviewer := spec([]string{planReady}, []string{changeDone, secondPass}, []string{verdictPass})
	v := &vcs{changed: []string{"a.go"}, head: "start"}
	c := &checks{results: [][]gate.Result{
		{{Name: "task test", Passed: false, Output: "--- FAIL: TestVersion"}},
		{{Name: "task test", Passed: true}},
	}}

	result := runWithChanges(t, s, v, c, []string{"a.go"})

	if result.Outcome != Proposed || result.Passes != 2 {
		t.Fatalf("result = %+v, want proposed on the second pass", result)
	}
	if len(reviewer.prompts) != 1 {
		t.Errorf("the review step ran %d times, want 1: a failing check is work, not a verdict", len(reviewer.prompts))
	}
	if !strings.Contains(engineer.prompts[1], "--- FAIL: TestVersion") {
		t.Errorf("the second prompt doesn't carry the failure:\n%s", engineer.prompts[1])
	}
}

func TestCommitsThatDoNotCheckOutAreNotMade(t *testing.T) {
	// The commit names a file the agent didn't change, so nothing can be committed.
	mismatch := `{"status":"complete","summary":"Added it.","commits":[{"message":"feat(cli): print the build commit","paths":["nowhere.go"],"fixes":""}],"addressed":[],"blockers":[]}`
	s, engineer, reviewer := spec([]string{planReady}, []string{mismatch, changeDone}, []string{verdictPass})
	v := &vcs{changed: []string{"a.go"}, head: "start"}
	c := &checks{results: [][]gate.Result{{{Name: "task test", Passed: true}}}}

	result := runWithChanges(t, s, v, c, []string{"a.go"})

	if result.Outcome != Proposed || result.Passes != 2 {
		t.Fatalf("result = %+v, want a second pass after the commits were refused", result)
	}
	if len(v.made) != 1 {
		t.Errorf("commits made = %q, want only the second pass's", v.made)
	}
	if len(reviewer.prompts) != 1 {
		t.Errorf("the review step ran %d times, want 1: there was nothing to review on the first pass", len(reviewer.prompts))
	}
	for _, want := range []string{"made none of the commits", "nowhere.go", "nothing to commit", "a.go"} {
		if !strings.Contains(engineer.prompts[1], want) {
			t.Errorf("the second prompt doesn't mention %q:\n%s", want, engineer.prompts[1])
		}
	}
}

func TestCommitsAnAgentMadeItselfAreTakenBack(t *testing.T) {
	s, engineer, _ := spec([]string{planReady}, []string{changeDone}, []string{verdictPass})
	v := &vcs{changed: []string{"a.go"}, head: "start"}
	engineer.onRun = func() { v.head = "the agent's own commit" }
	var progress strings.Builder

	result, err := New(v, &checks{}, &progress).Implement(context.Background(), s)
	if err != nil {
		t.Fatalf("Implement: %v", err)
	}
	if result.Outcome != Proposed {
		t.Fatalf("outcome = %s, want proposed", result.Outcome)
	}
	if len(v.resets) != 1 || v.resets[0] != "start" {
		t.Errorf("resets = %q, want the branch taken back to where the pass started", v.resets)
	}
	if len(v.made) != 1 {
		t.Errorf("commits made = %q, want the engine to have remade the work", v.made)
	}
}

func TestAttributionIsStrippedBeforeCommitting(t *testing.T) {
	withFooter := `{"status":"complete","summary":"Added it.","commits":[{"message":"feat(cli): print the build commit\n\nCo-authored-by: Claude <noreply@anthropic.com>","paths":["a.go"],"fixes":""}],"addressed":[],"blockers":[]}`
	s, _, _ := spec([]string{planReady}, []string{withFooter}, []string{verdictPass})
	v := &vcs{changed: []string{"a.go"}, head: "start"}

	run(t, s, v, nil)

	if len(v.made) != 1 {
		t.Fatalf("commits made = %q", v.made)
	}
	if strings.Contains(v.made[0], "Co-authored-by") {
		t.Errorf("the commit kept its attribution: %q", v.made[0])
	}
	if !strings.Contains(v.made[0], "feat(cli): print the build commit") {
		t.Errorf("the commit lost its message: %q", v.made[0])
	}
}

func TestPassesRunOut(t *testing.T) {
	s, engineer, _ := spec(
		[]string{planReady},
		[]string{changeDone, changeDone, changeDone},
		[]string{verdictFail, verdictFail, verdictFail},
	)
	s.MaxPasses = 3
	v := &vcs{changed: []string{"a.go"}, head: "start"}

	result := runWithChanges(t, s, v, &checks{}, []string{"a.go"})

	if result.Outcome != Exhausted || result.Passes != 3 {
		t.Fatalf("result = %+v, want exhausted after three passes", result)
	}
	if len(engineer.prompts) != 3 {
		t.Errorf("the change step ran %d times, want 3", len(engineer.prompts))
	}
	if len(result.Blockers) != 1 || !strings.Contains(result.Blockers[0], "no test covers the default") {
		t.Errorf("blockers = %q, want what was still outstanding", result.Blockers)
	}
}

func TestABlockedChangeStopsTheRun(t *testing.T) {
	blocked := `{"status":"blocked","summary":"Can't.","commits":[],"addressed":[],"blockers":["the API this needs was removed"]}`
	s, _, reviewer := spec([]string{planReady}, []string{blocked}, nil)
	v := &vcs{head: "start"}

	result := run(t, s, v, nil)

	if result.Outcome != Blocked || result.Passes != 1 {
		t.Fatalf("result = %+v, want blocked on the first pass", result)
	}
	if len(reviewer.prompts) != 0 {
		t.Error("the review step ran on a blocked change")
	}
	if len(result.Blockers) != 1 || !strings.Contains(result.Blockers[0], "API this needs") {
		t.Errorf("blockers = %q", result.Blockers)
	}
}

func TestImplementRefusesASpecItCannotRun(t *testing.T) {
	good, _, _ := spec([]string{planReady}, []string{changeDone}, []string{verdictPass})
	tests := []struct {
		name string
		edit func(*Spec)
		want string
	}{
		{name: "no workspace", edit: func(s *Spec) { s.Dir = "" }, want: "workspace, branch and base"},
		{name: "no passes", edit: func(s *Spec) { s.MaxPasses = 0 }, want: "max passes"},
		{name: "no agent", edit: func(s *Spec) { s.Verdict.Agent = nil }, want: "no agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := good
			tt.edit(&s)
			if _, err := New(&vcs{}, &checks{}, nil).Implement(context.Background(), s); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Implement = %v, want an error about %q", err, tt.want)
			}
		})
	}
}

// A gate on the plan step runs before anything is planned: planning against a tree that already fails is
// guesswork, so it blocks rather than becoming work.
func TestAFailedCheckBeforePlanningBlocksTheRun(t *testing.T) {
	s, engineer, _ := spec([]string{planReady}, []string{changeDone}, []string{verdictPass})
	s.Plan.Gate = [][]string{{"task", "build"}}
	c := &checks{results: [][]gate.Result{{{Name: "task build", Passed: false, Output: "undefined: Version"}}}}

	result := run(t, s, &vcs{head: "start"}, c)

	if result.Outcome != Blocked || result.Passes != 0 {
		t.Fatalf("result = %+v, want blocked before any pass", result)
	}
	if len(result.Blockers) != 1 || !strings.Contains(result.Blockers[0], "undefined: Version") {
		t.Errorf("blockers = %q, want the failing check", result.Blockers)
	}
	if len(engineer.prompts) != 0 {
		t.Error("the change step ran even though the base was broken")
	}
}

// A gate on the change step runs before it, and its results are what that step reads.
func TestAChangeStepReadsItsOwnChecks(t *testing.T) {
	s, engineer, _ := spec([]string{planReady}, []string{changeDone}, []string{verdictPass})
	s.Change.Gate = [][]string{{"task", "vet"}}
	c := &checks{results: [][]gate.Result{{{Name: "task vet", Passed: true, Output: "clean"}}}}

	result := run(t, s, &vcs{changed: []string{"a.go"}, head: "start"}, c)

	if result.Outcome != Proposed {
		t.Fatalf("outcome = %s, want proposed", result.Outcome)
	}
	if !strings.Contains(engineer.prompts[0], "task vet") {
		t.Errorf("the change prompt doesn't carry its own checks:\n%s", engineer.prompts[0])
	}
}

// recorder keeps what a run was asked and what it answered, in the order the steps ran.
type recorder struct {
	steps []string // "name: prompt -> answer"
	err   error
}

func (r *recorder) Step(name, prompt, answer string) error {
	r.steps = append(r.steps, fmt.Sprintf("%s: %s -> %s", name, prompt, answer))
	return r.err
}

// The run log is the only record of what an agent was told, so every step writes one, failures included.
func TestEveryStepIsRecorded(t *testing.T) {
	s, _, _ := spec(
		[]string{planReady},
		[]string{changeDone, `{"status":"complete","summary":"Added the test.","commits":[{"message":"test(cli): cover the default","paths":["a_test.go"],"fixes":""}],"addressed":[],"blockers":[]}`},
		[]string{verdictFail, verdictPass},
	)
	var log recorder
	s.Log = &log
	v := &vcs{changed: []string{"a.go"}, head: "origin/main"}

	runWithChanges(t, s, v, &checks{}, []string{"a_test.go"})

	var names []string
	for _, step := range log.steps {
		name, _, _ := strings.Cut(step, ":")
		names = append(names, name)
	}
	if got := strings.Join(names, ","); got != "plan,engineer,qa,engineer,qa" {
		t.Errorf("recorded steps = %s, want plan,engineer,qa,engineer,qa", got)
	}
	if !strings.Contains(log.steps[0], "Print the build commit") || !strings.Contains(log.steps[0], `"pr_title"`) {
		t.Errorf("the plan step recorded:\n%s\nwant the task in the prompt and the answer beside it", log.steps[0])
	}
}

// A step that failed is the one an operator most needs the prompt for.
func TestAFailedStepIsRecordedWithItsError(t *testing.T) {
	s, _, _ := spec(nil, nil, nil)
	s.Plan.Agent = &agent{err: fmt.Errorf("claude: exit status 1")}
	var log recorder
	s.Log = &log

	_, err := New(&vcs{head: "origin/main"}, &checks{}, nil).Implement(context.Background(), s)
	if err == nil {
		t.Fatal("Implement: want the step's error")
	}
	if len(log.steps) != 1 || !strings.Contains(log.steps[0], `{"error":"claude: exit status 1"}`) {
		t.Errorf("recorded = %q, want the failed plan step with its error as the answer", log.steps)
	}
}

// A run nobody can audit is worse than a run that stopped, so a log that can't be written stops it.
func TestARunStopsWhenItCannotBeRecorded(t *testing.T) {
	s, _, _ := spec([]string{planReady}, nil, nil)
	s.Log = &recorder{err: fmt.Errorf("no space left on device")}

	_, err := New(&vcs{head: "origin/main"}, &checks{}, nil).Implement(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "record plan") || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("error = %v, want the run to stop because the plan step couldn't be recorded", err)
	}
}
