package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/lifecycle"
	"github.com/stritech-oss/loomlc/internal/proc"
)

// checkText runs the repository's own commit policy checker over text loomlc would post. The fakes
// elsewhere in this package stand in for it; this is the authority, and the rules are anchored to the
// start of a line, which is the detail a fake gets wrong.
func checkText(t *testing.T, body string) error {
	t.Helper()
	script := filepath.Join("..", "..", "scripts", "commit-policy.sh")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("policy script not found: %v", err)
	}
	runner := proc.Exec{}
	if _, err := runner.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}

	file := filepath.Join(t.TempDir(), "body")
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatalf("write body: %v", err)
	}
	var out strings.Builder
	res, err := runner.Run(context.Background(), proc.Cmd{
		Name:   "bash",
		Args:   []string{script, "text", file},
		Env:    []string{"PATH=" + os.Getenv("PATH")},
		Stdout: &out,
		Stderr: &out,
	})
	if err != nil {
		t.Fatalf("run the policy script: %v", err)
	}
	if res.ExitCode != 0 {
		return &policyError{out.String()}
	}
	return nil
}

type policyError struct{ output string }

func (e *policyError) Error() string { return e.output }

// An agent writes the blockers, and loomlc posts them on the task under the operator's account. A
// trailer the policy refuses in a commit message has to be refused here too, including inside the list
// it is rendered as — which is where an anchored rule can't see it.
func TestAttributionNeverReachesTheTaskComment(t *testing.T) {
	attributions := []string{
		"Co-authored-by: Claude <noreply@anthropic.com>",
		"Assisted-by: Claude <noreply@anthropic.com>",
		"Claude-Session: https://claude.ai/code/abc",
		"Codex-Thread-Id: 99",
		"🤖 Generated with Claude Code",
		"Generated-by: Claude Code",
		"Generated using Codex",
		"See https://claude.ai/code",
		"https://chatgpt.com/codex/tasks/abc",
		"https://cursor.com/agents/1",
	}
	for _, line := range attributions {
		t.Run(line, func(t *testing.T) {
			blockers, dropped := sanitizeEach([]string{"the version package doesn't exist yet", line})
			if len(dropped) == 0 {
				t.Fatalf("%q survived the sanitizer", line)
			}
			body := Failure(Problem{Outcome: lifecycle.Blocked, Passes: 1, Blockers: blockers, Log: ".loomlc/runs/1"})

			if strings.Contains(body, line) {
				t.Errorf("the comment carries %q:\n%s", line, body)
			}
			if !strings.Contains(body, "the version package doesn't exist yet") {
				t.Errorf("the comment lost what the agent actually said:\n%s", body)
			}
			if err := checkText(t, body); err != nil {
				t.Errorf("the policy refused the comment: %v\n--- comment ---\n%s", err, body)
			}
		})
	}
}

// A multi-line blocker becomes one item with its later lines indented, and attribution on any of those
// lines goes the same way.
func TestAttributionInsideAMultiLineBlockerIsDropped(t *testing.T) {
	blockers, dropped := sanitizeEach([]string{"the build fails:\nundefined: Version\nGenerated with Claude Code"})
	if len(dropped) != 1 {
		t.Fatalf("dropped = %q, want the attribution line alone", dropped)
	}
	body := Failure(Problem{Outcome: lifecycle.Exhausted, Passes: 3, Blockers: blockers, Log: ".loomlc/runs/1"})

	if strings.Contains(body, "Generated with") {
		t.Errorf("the comment carries attribution:\n%s", body)
	}
	if !strings.Contains(body, "- the build fails:\n  undefined: Version") {
		t.Errorf("the comment lost the rest of the blocker:\n%s", body)
	}
	if err := checkText(t, body); err != nil {
		t.Errorf("the policy refused the comment: %v\n--- comment ---\n%s", err, body)
	}
}

// The same applies to the summary comment on a pull request, where the agent's plan is quoted.
func TestAttributionNeverReachesTheSummaryComment(t *testing.T) {
	result := passing()
	result.Plan.Summary = "Print the commit.\nGenerated with Claude Code"
	result.Plan.Deferred = []string{"a --json flag", "Co-authored-by: Claude <noreply@anthropic.com>"}

	o := NewOpener(&pusher{}, &outputs{}, &tasks{}, policyText{t})
	if _, err := o.Open(context.Background(), opened(), output(t), Decision{}, result); err != nil {
		t.Fatalf("Open: %v", err)
	}
	posted := o.outputs.(*outputs).comments
	if len(posted) != 1 {
		t.Fatalf("comments = %q, want the summary", posted)
	}
	for _, gone := range []string{"Generated with", "Co-authored-by"} {
		if strings.Contains(posted[0], gone) {
			t.Errorf("the summary carries %q:\n%s", gone, posted[0])
		}
	}
	if !strings.Contains(posted[0], "Print the commit.") || !strings.Contains(posted[0], "a --json flag") {
		t.Errorf("the summary lost what the agent actually said:\n%s", posted[0])
	}
}

// policyText is the real checker, as the Opener's policy.
type policyText struct{ t *testing.T }

func (p policyText) CheckText(_ context.Context, body string) error { return checkText(p.t, body) }
