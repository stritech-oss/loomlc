package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/lifecycle"
	"github.com/stritech-oss/loomlc/internal/proc/proctest"
	"github.com/stritech-oss/loomlc/internal/run"
	"github.com/stritech-oss/loomlc/internal/sink"
)

// feedbackIn runs `loomlc feedback` against a checkout, with fake as every subprocess it starts.
func feedbackIn(t *testing.T, dir string, fake *proctest.Fake, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	fake.On(proctest.Response{Stdout: dir + "\n"}, "git", "rev-parse")
	code := Main(context.Background(), append([]string{"feedback"}, args...), Env{
		Stdout:   &stdout,
		Stderr:   &stderr,
		ReadFile: os.ReadFile,
		LookPath: fake.LookPath,
		Environ:  func() []string { return []string{"HOME=/home/user"} },
		Runner:   fake,
		Dir:      dir,
	})
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// With no pull request asking for changes there is nothing to answer, so the command succeeds quietly
// and touches no labels.
func TestFeedbackStopsWhenNothingIsAskingForChanges(t *testing.T) {
	fake := ready()
	got := feedbackIn(t, checkout(t, checkoutFiles()), fake)

	if got.code != ExitOK || got.stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q", got.code, got.stderr)
	}
	if !strings.Contains(got.stdout, "Nothing to do") || !strings.Contains(got.stdout, "asking for changes") {
		t.Errorf("stdout = %q, want it to say nothing was asking for changes", got.stdout)
	}
	for _, call := range fake.Calls() {
		if argv := strings.Join(call.Argv(), " "); strings.Contains(argv, "pr edit") || strings.Contains(argv, "worktree add") {
			t.Errorf("ran %q, want no label touched and no workspace prepared", argv)
		}
	}
}

// The same preflight that stops a run stops a revision: these are the things an operator fixes once.
func TestFeedbackReportsWhatWouldStopItBeforeClaimingAnything(t *testing.T) {
	files := checkoutFiles()
	files["loomlc.yml"] = "sources:\n  github:\n    repo: acme/widgets\n"
	var fake proctest.Fake
	fake.SetPath("git", "/usr/bin/git")
	fake.On(proctest.Response{Stdout: ".git\n"}, "git")
	fake.On(proctest.Response{ExitCode: 2, Stderr: "error: No such remote 'origin'"}, "git", "remote", "get-url")

	got := feedbackIn(t, checkout(t, files), &fake)

	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want a failure", got.code)
	}
	for _, want := range []string{"sdlc can't run yet", "gh isn't on PATH"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr doesn't say %q:\n%s", want, got.stderr)
		}
	}
}

func TestFeedbackRejectsWhatCannotBeAnOutput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "two outputs", args: []string{"44", "45"}, want: "one output at a time"},
		{name: "an id with a flag in it", args: []string{"--add-label"}, want: "flag provided but not defined"},
		{name: "an id that isn't one", args: []string{"../../etc/passwd"}, want: "isn't an output id"},
		{name: "no passes", args: []string{"--max-iter", "-1"}, want: "--max-iter must be at least 1"},
		{name: "a configuration nobody named", args: []string{"--config", ""}, want: "--config needs a path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Main(context.Background(), append([]string{"feedback"}, tt.args...), Env{Stdout: &stdout, Stderr: &stderr})
			if code != ExitUsage {
				t.Errorf("exit code = %d, want %d", code, ExitUsage)
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("stderr = %q, want it to say %q", stderr.String(), tt.want)
			}
		})
	}
}

// An output printed as the sink prints it is accepted; one from a sink this lifecycle doesn't publish to
// is refused, rather than looked up in the wrong place.
func TestFeedbackChecksTheOutputBelongsToTheLifecyclesSink(t *testing.T) {
	got := feedbackIn(t, checkout(t, checkoutFiles()), ready(), "gitlab-mr#44")

	if got.code != ExitUsage {
		t.Fatalf("exit code = %d, want a usage error", got.code)
	}
	if !strings.Contains(got.stderr, `publishes to "github-pr", not "gitlab-mr"`) {
		t.Errorf("stderr = %q, want it to name the sink the lifecycle has", got.stderr)
	}
}

func TestFeedbackHelpSaysWhatItDoes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"feedback", "--help"}, Env{Stdout: &stdout, Stderr: &stderr})

	if code != ExitOK || stderr.String() != "" {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{"loomlc feedback [flags] [output]", "fixup! commit", "--autosquash", "-max-iter"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help doesn't mention %q:\n%s", want, stdout.String())
		}
	}
}

// What the terminal says has to match what the exit code says, and both have to match what was posted
// on the pull request: a script gating on the exit code can't be told a refused push succeeded.
func TestDescribeRevisionMatchesTheExitCode(t *testing.T) {
	pushed := []git.Commit{{SHA: "2222222222222222", Subject: "fixup! feat(cli): print it"}}
	tests := map[string]struct {
		rev      run.Revision
		answered bool
		want     []string
		absent   []string
	}{
		"pushed and answered": {
			rev:      run.Revision{Output: ref(), URL: url, Base: "main", Outcome: lifecycle.Proposed, Replied: true, Claimed: true, Pushed: true, Commits: pushed, Fixups: 1},
			answered: true,
			want:     []string{"Answered the review on " + url, "pushed 1 commit", "--autosquash main"},
		},
		"answered in words": {
			rev:      run.Revision{Output: ref(), URL: url, Outcome: lifecycle.Proposed, Replied: true, Claimed: true},
			answered: true,
			want:     []string{"nothing on the branch needed to change"},
		},
		"the push was refused": {
			rev:      run.Revision{Output: ref(), URL: url, Outcome: lifecycle.Proposed, Replied: true, Claimed: true, Commits: pushed, Refusals: []string{"loomlc.yml is protected and this change modifies it"}},
			answered: false,
			want:     []string{"Nothing was pushed to github-pr#44", "loomlc.yml is protected"},
			absent:   []string{"Answered the review"},
		},
		"pushed but the reviewer wasn't told": {
			rev:      run.Revision{Output: ref(), URL: url, Outcome: lifecycle.Proposed, Claimed: true, Pushed: true, Commits: pushed},
			answered: false,
			want:     []string{"Pushed 1 commit to github-pr#44", "Nothing was said on github-pr#44"},
			absent:   []string{"Answered the review"},
		},
		"no task to revise against": {
			rev:      run.Revision{Output: ref(), URL: url, Replied: true, Blockers: []string{"github-pr#44 closes no issue in this repository"}},
			answered: false,
			want:     []string{"Nothing was pushed", "closes no issue"},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if tt.rev.Answered() != tt.answered {
				t.Errorf("Answered() = %v, want %v", tt.rev.Answered(), tt.answered)
			}
			got := describeRevision(tt.rev)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("output doesn't say %q:\n%s", want, got)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(got, absent) {
					t.Errorf("output claims %q:\n%s", absent, got)
				}
			}
		})
	}
}

const url = "https://github.com/acme/widgets/pull/44"

func ref() sink.Ref { return sink.Ref{Sink: "github-pr", ID: "44"} }
