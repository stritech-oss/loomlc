package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/proc"
)

// A run's own work — its branch, its commits, its push — is only as real as the git that did it, so this
// test runs git and the commit policy checker for real against a local origin, and fakes only what
// reaches the network: gh, claude, and the lifecycle's check.
type endToEnd struct {
	t *testing.T
	// base holds everything; origin is the bare repository, repo the operator's checkout.
	base, origin, repo string
	env                []string
	// answers are claude's structured answers, in the order the steps ask for them.
	answers []string
	// edits are the files claude writes into the workspace before answering, keyed by the answer index.
	edits map[int]map[string]string
	// calls is every argv a run started, and sent the stdin each one was given, at the same index.
	calls, sent []string
	runs        int
}

func newEndToEnd(t *testing.T) *endToEnd {
	t.Helper()
	base := t.TempDir()
	e := &endToEnd{
		t:      t,
		base:   base,
		origin: filepath.Join(base, "origin.git"),
		repo:   filepath.Join(base, "repo"),
		env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + base,
			"GIT_CONFIG_GLOBAL=" + os.DevNull,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Ada Human",
			"GIT_AUTHOR_EMAIL=ada@example.com",
			"GIT_COMMITTER_NAME=Ada Human",
			"GIT_COMMITTER_EMAIL=ada@example.com",
		},
		edits: map[int]map[string]string{},
	}

	e.git(base, "init", "--quiet", "--bare", "--initial-branch=main", e.origin)
	e.git(base, "clone", "--quiet", e.origin, e.repo)
	e.write(e.repo, "README.md", "hello\n")
	e.git(e.repo, "add", "README.md")
	e.git(e.repo, "commit", "--quiet", "--message", "docs: start")
	e.git(e.repo, "push", "--quiet", "origin", "main")

	// What loomlc reads from the operator's checkout rather than from the workspace: its own commit
	// policy checker, and the repository's pull request template.
	e.write(e.repo, "scripts/commit-policy.sh", e.read("../../scripts/commit-policy.sh"))
	e.write(e.repo, ".github/pull_request_template.md", "## Done / Description\n\n## QA\n\n## Issue\n")
	e.write(e.repo, "loomlc.yml", `sources:
  github:
    repo: acme/widgets
sinks:
  github-pr:
    reviewer: ""
lifecycles:
  sdlc:
    publish:
      gates: ["check all"]
`)
	return e
}

func (e *endToEnd) git(dir string, args ...string) {
	e.t.Helper()
	var out bytes.Buffer
	res, err := proc.Exec{}.Run(context.Background(), proc.Cmd{Name: "git", Args: args, Dir: dir, Env: e.env, Stdout: &out, Stderr: &out})
	if err != nil || res.ExitCode != 0 {
		e.t.Fatalf("git %s: %v (exit %d)\n%s", strings.Join(args, " "), err, res.ExitCode, out.String())
	}
}

// gitOut returns git's output, for asking the origin what a run pushed to it.
func (e *endToEnd) gitOut(dir string, args ...string) string {
	e.t.Helper()
	var out bytes.Buffer
	res, err := proc.Exec{}.Run(context.Background(), proc.Cmd{Name: "git", Args: args, Dir: dir, Env: e.env, Stdout: &out, Stderr: &out})
	if err != nil || res.ExitCode != 0 {
		e.t.Fatalf("git %s: %v (exit %d)\n%s", strings.Join(args, " "), err, res.ExitCode, out.String())
	}
	return strings.TrimRight(out.String(), "\n")
}

func (e *endToEnd) write(dir, name, content string) {
	e.t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		e.t.Fatalf("make %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatalf("write %s: %v", path, err)
	}
}

func (e *endToEnd) read(path string) string {
	e.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// LookPath implements proc.Runner: the three executables a run needs are installed.
func (e *endToEnd) LookPath(name string) (string, error) {
	switch name {
	case "git", "gh", "claude":
		return "/usr/bin/" + name, nil
	}
	return "", fmt.Errorf("look up %s: %w", name, proc.ErrNotFound)
}

// Run implements proc.Runner.
func (e *endToEnd) Run(ctx context.Context, c proc.Cmd) (proc.Result, error) {
	argv := strings.Join(append([]string{c.Name}, c.Args...), " ")
	// A provider's role prompt and output schema are arguments too, and they are pages of prose. The
	// transcript keeps the command without them, so searching it finds commands rather than words.
	if i := slices.Index(c.Args, "--append-system-prompt"); i >= 0 {
		argv = strings.Join(append([]string{c.Name}, c.Args[:i]...), " ")
	}
	stdin := ""
	if c.Stdin != nil {
		b, err := io.ReadAll(c.Stdin)
		if err != nil {
			return proc.Result{}, err
		}
		stdin = string(b)
	}
	e.calls, e.sent = append(e.calls, argv), append(e.sent, stdin)

	// The provider resolves its executable on PATH first, so the command arrives as a path.
	switch filepath.Base(c.Name) {
	case "git", "bash":
		return proc.Exec{}.Run(ctx, c)
	case "claude":
		return e.claude(c)
	case "gh":
		return e.gh(c)
	case "check":
		return proc.Result{ExitCode: 0}, nil // the lifecycle's own check, which this test isn't about
	}
	return proc.Result{ExitCode: -1}, fmt.Errorf("the run started %s, which this test doesn't fake", c.Name)
}

// claude answers the next step, after writing whatever that step's agent would have written.
func (e *endToEnd) claude(c proc.Cmd) (proc.Result, error) {
	if e.runs >= len(e.answers) {
		return proc.Result{ExitCode: 1}, fmt.Errorf("claude ran %d times, and only %d answers are scripted", e.runs+1, len(e.answers))
	}
	i := e.runs
	e.runs++
	for name, content := range e.edits[i] {
		e.write(c.Dir, name, content)
	}
	result, err := json.Marshal(map[string]any{
		"is_error":          false,
		"session_id":        fmt.Sprintf("00000000-0000-4000-8000-00000000000%d", i),
		"total_cost_usd":    0.01,
		"result":            e.answers[i],
		"structured_output": json.RawMessage(e.answers[i]),
	})
	if err != nil {
		return proc.Result{}, err
	}
	return e.reply(c, string(result))
}

// gh answers the forge's questions: one issue is ready, and the pull request it ends up opening is 45.
func (e *endToEnd) gh(c proc.Cmd) (proc.Result, error) {
	args := strings.Join(c.Args, " ")
	switch {
	case strings.HasPrefix(args, "pr list"):
		return e.reply(c, "[]")
	case strings.HasPrefix(args, "issue list") && strings.Contains(args, "agent-ready"):
		return e.reply(c, `[{"number":9,"labels":[{"name":"agent-ready"}]}]`)
	case strings.HasPrefix(args, "issue list"):
		return e.reply(c, "[]")
	case strings.HasPrefix(args, "issue view"):
		return e.reply(c, `{"number":9,"title":"Print the build commit","body":"loomlc version should print the commit it was built from.","url":"https://github.com/acme/widgets/issues/9","labels":[{"name":"agent-ready"}],"state":"OPEN","stateReason":""}`)
	case strings.HasPrefix(args, "pr create"):
		return e.reply(c, "https://github.com/acme/widgets/pull/45\n")
	case strings.HasPrefix(args, "issue edit"), strings.HasPrefix(args, "issue comment"), strings.HasPrefix(args, "pr comment"):
		return e.reply(c, "")
	}
	return proc.Result{ExitCode: -1}, fmt.Errorf("the run ran gh %s, which this test doesn't fake", args)
}

func (e *endToEnd) reply(c proc.Cmd, out string) (proc.Result, error) {
	if c.Stdout != nil {
		if _, err := c.Stdout.Write([]byte(out)); err != nil {
			return proc.Result{}, err
		}
	}
	return proc.Result{ExitCode: 0}, nil
}

// main runs the command line the way the process does.
func (e *endToEnd) main(args ...string) result {
	e.t.Helper()
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), args, Env{
		Stdout:   &stdout,
		Stderr:   &stderr,
		ReadFile: os.ReadFile,
		LookPath: e.LookPath,
		Environ:  func() []string { return e.env },
		Runner:   e,
		Dir:      e.repo,
	})
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// ran reports whether any command started with want. It matches the start of the argv rather than
// anywhere in it, because a provider's deny list mentions `git push`, and that isn't a push.
func (e *endToEnd) ran(want string) bool {
	for _, call := range e.calls {
		if strings.HasPrefix(call, want) {
			return true
		}
	}
	return false
}

// posted returns the text written to the stdin of the first command whose argv contains want, which is
// how gh is given a description or a comment.
func (e *endToEnd) posted(want string) string {
	for i, call := range e.calls {
		if strings.HasPrefix(call, want) {
			return e.sent[i]
		}
	}
	e.t.Fatalf("nothing ran matching %q", want)
	return ""
}

// The answers a run gets from its steps: a plan, one commit's worth of work, and a review that passes.
const (
	planAnswer   = `{"status":"ready","pr_title":"feat(cli): print the build commit","summary":"Print the commit the binary was built from.","files":[{"path":"version.txt","change":"record the commit"}],"tests":["the stamped value"],"deferred":[],"blockers":[]}`
	changeAnswer = `{"status":"complete","summary":"Recorded the commit the binary was built from.","commits":[{"message":"feat(cli): print the build commit","paths":["version.txt"],"fixes":""}],"addressed":[],"blockers":[]}`
	verdictPass  = `{"verdict":"pass","summary":"The check passes and the change does what the plan said.","findings":[]}`
)

func TestRunTakesATaskAllTheWayToAPullRequest(t *testing.T) {
	e := newEndToEnd(t)
	e.answers = []string{planAnswer, changeAnswer, verdictPass}
	e.edits[1] = map[string]string{"version.txt": "the commit\n"}

	got := e.main("run")

	if got.code != ExitOK || got.stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q\nstdout:\n%s", got.code, got.stderr, got.stdout)
	}
	if !strings.Contains(got.stdout, "Proposed in https://github.com/acme/widgets/pull/45") {
		t.Errorf("stdout doesn't say where the work went:\n%s", got.stdout)
	}

	// The branch on the origin is what a reviewer will read: the run's own commit, signed off by the
	// operator, on top of the base.
	branch := "feat/issue-9-print-the-build-commit"
	log := e.gitOut(e.origin, "log", "--format=%s%n%b", "main.."+branch)
	if !strings.Contains(log, "feat(cli): print the build commit") {
		t.Errorf("the pushed branch doesn't carry the run's commit:\n%s", log)
	}
	if !strings.Contains(log, "Signed-off-by: Ada Human <ada@example.com>") {
		t.Errorf("the pushed commit isn't signed off by the operator:\n%s", log)
	}
	if files := e.gitOut(e.origin, "show", "--name-only", "--format=", branch); files != "version.txt" {
		t.Errorf("the commit touches %q, want only the file the change step wrote", files)
	}

	// The description is the repository's own template, filled in.
	body := e.posted("gh pr create")
	for _, want := range []string{"## Done / Description", "Recorded the commit the binary was built from.", "## QA", "The check passes", "Closes #9"} {
		if !strings.Contains(body, want) {
			t.Errorf("the description doesn't contain %q:\n%s", want, body)
		}
	}

	// The task is claimed on the way in and marked done on the way out, and both comments are loomlc's.
	if !e.ran("gh issue edit 9 --repo acme/widgets --add-label agent-in-progress") {
		t.Error("the task was never claimed")
	}
	if !e.ran("gh issue edit 9 --repo acme/widgets --remove-label agent-in-progress --add-label agent-done") {
		t.Error("the task was never marked done")
	}
	if link := e.posted("gh issue comment"); !strings.Contains(link, "pull/45") {
		t.Errorf("the task wasn't told where the work went: %q", link)
	}
	if summary := e.posted("gh pr comment"); !strings.Contains(summary, "claude") {
		t.Errorf("the summary comment doesn't say what ran the steps:\n%s", summary)
	}

	// The run left a record of itself, and gave the workspace back.
	runs, err := os.ReadDir(filepath.Join(e.repo, ".loomlc", "runs"))
	if err != nil || len(runs) != 1 {
		t.Fatalf("run records = %v (%v), want one", runs, err)
	}
	dir := filepath.Join(e.repo, ".loomlc", "runs", runs[0].Name())
	record := e.read(filepath.Join(dir, "run.json"))
	for _, want := range []string{`"outcome": "proposed"`, "pull/45", `"passes": 1`, branch} {
		if !strings.Contains(record, want) {
			t.Errorf("run.json doesn't record %q:\n%s", want, record)
		}
	}
	steps, err := filepath.Glob(filepath.Join(dir, "*-prompt.md"))
	if err != nil || len(steps) != 3 {
		t.Errorf("recorded prompts = %v (%v), want one per step", steps, err)
	}
	if _, err := os.Stat(filepath.Join(e.repo, ".loomlc", "worktrees", "github-9")); !os.IsNotExist(err) {
		t.Errorf("the workspace is still there: %v", err)
	}
}

// A plan that says the work can't be done stops the run before anything is built, which is the cheapest
// possible ending: the task hears why, and nothing is pushed.
func TestRunStopsOnABlockedPlanWithoutPushing(t *testing.T) {
	e := newEndToEnd(t)
	e.answers = []string{`{"status":"blocked","pr_title":"feat(cli): print the build commit","summary":"Can't.","files":[],"tests":[],"deferred":[],"blockers":["the version package doesn't exist yet"]}`}

	got := e.main("run")

	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want a failure\nstdout:\n%s", got.code, got.stdout)
	}
	if !strings.Contains(got.stdout, "the version package doesn't exist yet") {
		t.Errorf("stdout doesn't say what stopped it:\n%s", got.stdout)
	}
	if e.ran("git push") || e.ran("gh pr create") {
		t.Error("something was pushed or proposed for a plan that was blocked")
	}
	comment := e.posted("gh issue comment")
	for _, want := range []string{"<!-- loomlc:", "the version package doesn't exist yet", ".loomlc/runs/"} {
		if !strings.Contains(comment, want) {
			t.Errorf("the comment on the task doesn't say %q:\n%s", want, comment)
		}
	}
	if !e.ran("gh issue edit 9 --repo acme/widgets --remove-label agent-in-progress --add-label agent-failed") {
		t.Error("the task wasn't marked failed")
	}
}

// --max-iter bounds the loop, and a review that never passes ends the run with the review's own findings
// on the task rather than a pull request.
func TestRunStopsWhenTheReviewNeverPasses(t *testing.T) {
	e := newEndToEnd(t)
	e.answers = []string{planAnswer, changeAnswer, `{"verdict":"fail","summary":"Not yet.","findings":[{"detail":"no test covers the stamped value","path":"version.txt","line":1}]}`}
	e.edits[1] = map[string]string{"version.txt": "the commit\n"}

	got := e.main("run", "--max-iter", "1")

	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want a failure\nstdout:\n%s", got.code, got.stdout)
	}
	if !strings.Contains(got.stdout, "no test covers the stamped value") {
		t.Errorf("stdout doesn't carry the review's finding:\n%s", got.stdout)
	}
	if e.ran("gh pr create") {
		t.Error("work that never passed review was proposed")
	}
	if comment := e.posted("gh issue comment"); !strings.Contains(comment, "didn't pass review in 1 pass") {
		t.Errorf("the comment doesn't say the loop ran out:\n%s", comment)
	}
	// The run's commit stays on the branch for the next attempt, even though nothing was pushed.
	if log := e.gitOut(e.repo, "log", "--format=%s", "origin/main..feat/issue-9-print-the-build-commit"); !strings.Contains(log, "feat(cli): print the build commit") {
		t.Errorf("the work was lost: %q", log)
	}
}

// Naming a task runs that task: the ready list isn't consulted at all.
func TestRunTakesTheTaskItIsNamed(t *testing.T) {
	e := newEndToEnd(t)
	e.answers = []string{planAnswer, changeAnswer, verdictPass}
	e.edits[1] = map[string]string{"version.txt": "the commit\n"}

	got := e.main("run", "9")

	if got.code != ExitOK || !strings.Contains(got.stdout, "pull/45") {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", got.code, got.stdout, got.stderr)
	}
	for _, call := range e.calls {
		if strings.HasPrefix(call, "gh issue list") || strings.HasPrefix(call, "gh pr list --repo acme/widgets --label") {
			t.Errorf("ran %q, want a named task taken without asking what else is ready", call)
		}
	}
}

// Work that passed review can still be refused: a run may not change a protected path. The commits stay
// on the branch, nothing is pushed, and the issue is told why.
func TestRunRefusesToPublishAChangeToAProtectedPath(t *testing.T) {
	e := newEndToEnd(t)
	e.answers = []string{
		planAnswer,
		`{"status":"complete","summary":"Changed the configuration.","commits":[{"message":"feat(cli): print the build commit","paths":["loomlc.yml"],"fixes":""}],"addressed":[],"blockers":[]}`,
		verdictPass,
	}
	e.edits[1] = map[string]string{"loomlc.yml": "sources:\n  github:\n    repo: someone/else\n"}

	got := e.main("run")

	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want a failure\nstdout:\n%s", got.code, got.stdout)
	}
	if e.ran("git push") || e.ran("gh pr create") {
		t.Error("a change to a protected path was pushed")
	}
	if !strings.Contains(got.stdout, "loomlc.yml is protected") {
		t.Errorf("stdout doesn't say what was refused:\n%s", got.stdout)
	}
	comment := e.posted("gh issue comment")
	for _, want := range []string{"wouldn't publish it", "loomlc.yml is protected"} {
		if !strings.Contains(comment, want) {
			t.Errorf("the comment doesn't say %q:\n%s", want, comment)
		}
	}
	if !e.ran("gh issue edit 9 --repo acme/widgets --remove-label agent-in-progress --add-label agent-failed") {
		t.Error("the task wasn't marked failed")
	}
}
