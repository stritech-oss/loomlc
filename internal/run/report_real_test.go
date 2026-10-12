package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/lifecycle"
	"github.com/stritech-oss/loomlc/internal/proc"
	"github.com/stritech-oss/loomlc/internal/prompt"
	"github.com/stritech-oss/loomlc/internal/sink"
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

// A reviewer's comment and an agent's answer to it both end up in one bullet of the reply, where an
// anchored rule can't see a trailer that the "- " pushed off the start of the line.
func TestAttributionNeverReachesTheReply(t *testing.T) {
	attributions := []string{
		"Co-authored-by: Claude <noreply@anthropic.com>",
		"Claude-Session: https://claude.ai/code/abc",
		"🤖 Generated with Claude Code",
		"See https://claude.ai/code",
		"https://chatgpt.com/codex/tasks/abc",
	}
	for _, line := range attributions {
		t.Run(line, func(t *testing.T) {
			addressed, dropped := sanitizeAddressed([]prompt.Addressed{
				{Item: "the flag is read twice", Response: "Read it once.\n" + line},
				{Item: line, Response: "Declined: that is attribution."},
			})
			if len(dropped) < 2 {
				t.Fatalf("dropped = %q, want it gone from both the item and the response", dropped)
			}
			rev := Revision{
				Output:    sink.Ref{Sink: "github-pr", ID: "44"},
				Outcome:   lifecycle.Proposed,
				Passes:    1,
				Addressed: addressed,
				Commits:   []git.Commit{{SHA: "2222222222222222", Subject: "fixup! feat(cli): print it"}},
				Fixups:    1,
				Pushed:    true,
				Log:       ".loomlc/runs/1",
			}
			body := Reply(rev)

			if strings.Contains(body, line) {
				t.Errorf("the reply carries %q:\n%s", line, body)
			}
			if !strings.Contains(body, "Read it once.") {
				t.Errorf("the reply lost what the agent actually said:\n%s", body)
			}
			if err := checkText(t, body); err != nil {
				t.Errorf("the policy refused the reply: %v\n--- reply ---\n%s", err, body)
			}
		})
	}
}

// The reply quotes commit subjects, and git writes a fixup's subject itself: "fixup! " in front of the
// subject of the commit it fixes. The attribution rules are anchored to the start of a line, so a
// trailer behind that prefix is invisible to the policy script — which makes the sanitizer, not the
// script, the thing that has to catch it.
func TestAttributionBehindAnAutosquashPrefixIsDropped(t *testing.T) {
	for _, prefix := range []string{"fixup! ", "squash! ", "amend! "} {
		t.Run(strings.TrimSpace(prefix), func(t *testing.T) {
			subject := prefix + "Co-authored-by: Claude <noreply@anthropic.com>"
			clean, dropped := sanitizeSubject(subject)
			if len(dropped) == 0 {
				t.Fatalf("%q survived the sanitizer", subject)
			}
			if strings.Contains(clean, "Co-authored-by") {
				t.Errorf("subject = %q, want the trailer gone", clean)
			}

			body := Reply(Revision{
				Output:  sink.Ref{Sink: "github-pr", ID: "44"},
				Base:    "main",
				Outcome: lifecycle.Proposed,
				Passes:  1,
				Commits: []git.Commit{{SHA: "2222222222222222", Subject: clean}},
				Fixups:  1,
				Pushed:  true,
				Log:     ".loomlc/runs/1",
			})
			if strings.Contains(body, "Co-authored-by") {
				t.Errorf("the reply carries the trailer:\n%s", body)
			}
			if err := checkText(t, body); err != nil {
				t.Errorf("the policy refused the reply: %v\n--- reply ---\n%s", err, body)
			}
		})
	}
}

// policyText is the real checker, as the Opener's policy.
type policyText struct{ t *testing.T }

func (p policyText) CheckText(_ context.Context, body string) error { return checkText(p.t, body) }
