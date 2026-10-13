package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/proc/proctest"
	"github.com/stritech-oss/loomlc/internal/run"
)

// tidyIn runs `loomlc tidy` against a checkout, with fake as every subprocess it starts.
func tidyIn(t *testing.T, dir string, fake *proctest.Fake, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	fake.On(proctest.Response{Stdout: dir + "\n"}, "git", "rev-parse")
	code := Main(context.Background(), append([]string{"tidy"}, args...), Env{
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

func TestTidyRejectsWhatCannotBeAnOutput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no output", args: nil, want: "name one pull request"},
		{name: "two outputs", args: []string{"44", "45"}, want: "name one pull request"},
		{name: "an id with a flag in it", args: []string{"--force"}, want: "flag provided but not defined"},
		{name: "an id that isn't one", args: []string{"../../etc/passwd"}, want: "isn't an output id"},
		{name: "a configuration nobody named", args: []string{"--config", "", "44"}, want: "--config needs a path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Main(context.Background(), append([]string{"tidy"}, tt.args...), Env{Stdout: &stdout, Stderr: &stderr})
			if code != ExitUsage {
				t.Errorf("exit code = %d, want %d", code, ExitUsage)
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("stderr = %q, want it to say %q", stderr.String(), tt.want)
			}
		})
	}
}

// Folding runs no agent, so a provider that isn't installed doesn't stop it: the person merging needs
// git and the forge, and nothing else. This also walks the exit code all the way out, for a pull
// request loomlc won't rewrite: the fixups are still on it, so a script that folds before merging has
// to be told no.
func TestTidyRunsWithoutTheProvidersInstalled(t *testing.T) {
	var fake proctest.Fake
	for _, cmd := range []string{"git", "gh"} {
		fake.SetPath(cmd, "/usr/bin/"+cmd)
	}
	fake.On(proctest.Response{Stdout: "git@github.com:acme/widgets.git\n"}, "git", "remote", "get-url")
	// The pull request has been merged already, which is as far as this needs to get: the point is that
	// it got past the checks an operator's machine has to pass.
	fake.On(proctest.Response{Stdout: `{"number":44,"url":"` + url + `","state":"MERGED","headRefName":"feat/issue-9","baseRefName":"main","isCrossRepository":false,"labels":[]}`}, "gh", "pr", "view")

	got := tidyIn(t, checkout(t, checkoutFiles()), &fake, "44")

	if got.code != ExitFailure || got.stderr != "" {
		t.Fatalf("exit code = %d, want %d; stderr = %q\nstdout:\n%s", got.code, ExitFailure, got.stderr, got.stdout)
	}
	if !strings.Contains(got.stdout, "is closed") {
		t.Errorf("stdout = %q, want it to say the pull request is closed", got.stdout)
	}
	for _, call := range fake.Calls() {
		if argv := strings.Join(call.Argv(), " "); strings.Contains(argv, "worktree add") || strings.Contains(argv, "push") {
			t.Errorf("ran %q, want nothing touched on a closed pull request", argv)
		}
	}
}

// An output from a sink this lifecycle doesn't publish to is refused rather than looked up in the wrong
// place.
func TestTidyChecksTheOutputBelongsToTheLifecyclesSink(t *testing.T) {
	got := tidyIn(t, checkout(t, checkoutFiles()), ready(), "gitlab-mr#44")

	if got.code != ExitUsage {
		t.Fatalf("exit code = %d, want a usage error", got.code)
	}
	if !strings.Contains(got.stderr, `publishes to "github-pr", not "gitlab-mr"`) {
		t.Errorf("stderr = %q, want it to name the sink the lifecycle has", got.stderr)
	}
}

func TestTidyHelpSaysWhatItDoes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"tidy", "--help"}, Env{Stdout: &stdout, Stderr: &stderr})

	if code != ExitOK || stderr.String() != "" {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{"loomlc tidy [flags] <output>", "byte-identical", "-lifecycle"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help doesn't mention %q:\n%s", want, stdout.String())
		}
	}
}

// What the terminal says has to match what the exit code says: a script that tidies before merging
// can't be told a branch is ready when the fixups are still on it.
func TestDescribeTidySaysWhatHappened(t *testing.T) {
	after := []git.Commit{{SHA: "3333333333333333", Subject: "feat(cli): print it"}}
	tests := map[string]struct {
		res    run.Tidy
		folded bool
		want   []string
		absent []string
	}{
		"folded and pushed": {
			res:    run.Tidy{Output: ref(), URL: url, Fixups: 1, After: after, Pushed: true, Commented: true},
			folded: true,
			want:   []string{"Folded 1 fix into the 1 commit on " + url, "force-pushed"},
		},
		"nothing to fold": {
			res:  run.Tidy{Output: ref(), URL: url, Idle: "github-pr#44 has no fixup commits to fold"},
			want: []string{"Nothing to do", "no fixup commits"},
		},
		"the fold didn't apply": {
			res: run.Tidy{Output: ref(), URL: url, Fixups: 2, Commented: true,
				Refusals: []string{"the fixups didn't fold into the commits they name; git's own account of it is in the operator's terminal"}},
			want:   []string{"Nothing was folded on github-pr#44", "didn't fold into the commits they name"},
			absent: []string{"Folded 2", "Nothing was said"},
		},
		"the push failed": {
			res:    run.Tidy{Output: ref(), URL: url, Fixups: 1},
			want:   []string{"Nothing was folded on github-pr#44", "Nothing was said on github-pr#44"},
			absent: []string{"Folded 1"},
		},
		"loomlc wouldn't fold it": {
			res:  run.Tidy{Output: ref(), URL: url, Refusals: []string{"github-pr#44 is closed, so there is nothing to fold"}},
			want: []string{"Nothing was folded on github-pr#44", "is closed"},
			// It was never looked at, so there is nothing loomlc owed the pull request.
			absent: []string{"Folded", "Nothing was said"},
		},
		"folded, but the pull request wasn't told": {
			res:    run.Tidy{Output: ref(), URL: url, Fixups: 1, After: after, Pushed: true},
			folded: true,
			want:   []string{"Folded 1 fix", "Nothing was said on github-pr#44 about it"},
		},
		"no pull request was looked at": {
			res: run.Tidy{},
			// The error says why, and saying anything here would name a pull request loomlc never read.
			absent: []string{"Nothing", "Folded", "#"},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if tt.res.Folded() != tt.folded {
				t.Errorf("Folded() = %v, want %v", tt.res.Folded(), tt.folded)
			}
			got := describeTidy(tt.res)
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
