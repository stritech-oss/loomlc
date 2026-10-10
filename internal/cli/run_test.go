package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/proc/proctest"
)

// checkout writes the files a run reads from the operator's own checkout, and returns its path.
func checkout(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("make %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return dir
}

// runnable is a configuration that can run: a repository to take tasks from, and a gate that builds the
// work.
const runnable = `sources:
  github:
    repo: acme/widgets
lifecycles:
  sdlc:
    publish:
      gates: ["task check"]
`

// template is the repository's pull request template, with the headings the preset's sink names.
const template = "## Done / Description\n\n## QA\n\n## Issue\n"

func checkoutFiles() map[string]string {
	return map[string]string{
		"loomlc.yml":                       runnable,
		"scripts/commit-policy.sh":         "#!/usr/bin/env bash\nexit 0\n",
		".github/pull_request_template.md": template,
	}
}

// ready returns a fake with git, gh and claude installed, answering the questions a run asks before it
// claims anything.
func ready() *proctest.Fake {
	var fake proctest.Fake
	for _, cmd := range []string{"git", "gh", "claude"} {
		fake.SetPath(cmd, "/usr/bin/"+cmd)
	}
	fake.On(proctest.Response{Stdout: "git@github.com:acme/widgets.git\n"}, "git", "remote", "get-url")
	fake.On(proctest.Response{Stdout: "[]\n"}, "gh")
	return &fake
}

// runIn runs the command line against a checkout, with fake as every subprocess it starts. The
// configuration is left to be found in the checkout, which is where a run reads everything else from.
func runIn(t *testing.T, dir string, fake *proctest.Fake, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	// The checkout is where git says the repository's root is, which preflight checks.
	fake.On(proctest.Response{Stdout: dir + "\n"}, "git", "rev-parse")
	code := Main(context.Background(), append([]string{"run"}, args...), Env{
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

// With nothing ready there is nothing to report and nothing to fix, so the command succeeds quietly and
// claims nothing.
func TestRunStopsWhenNothingIsReady(t *testing.T) {
	fake := ready()
	got := runIn(t, checkout(t, checkoutFiles()), fake)

	if got.code != ExitOK || got.stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q", got.code, got.stderr)
	}
	if !strings.Contains(got.stdout, "Nothing to do") || !strings.Contains(got.stdout, "nothing is ready") {
		t.Errorf("stdout = %q, want it to say there was nothing to pick up", got.stdout)
	}
	for _, call := range fake.Calls() {
		if argv := strings.Join(call.Argv(), " "); strings.Contains(argv, "issue edit") || strings.Contains(argv, "worktree add") {
			t.Errorf("ran %q, want nothing claimed or prepared", argv)
		}
	}
}

// Everything an operator has to fix comes back at once, before a task is claimed.
func TestRunReportsWhatWouldStopItBeforeClaimingAnything(t *testing.T) {
	files := checkoutFiles()
	files["loomlc.yml"] = "sources:\n  github:\n    repo: acme/widgets\n"
	var fake proctest.Fake
	fake.SetPath("git", "/usr/bin/git")
	fake.On(proctest.Response{Stdout: ".git\n"}, "git")
	fake.On(proctest.Response{ExitCode: 2, Stderr: "error: No such remote 'origin'"}, "git", "remote", "get-url")

	got := runIn(t, checkout(t, files), &fake)

	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want a failure", got.code)
	}
	for _, want := range []string{"sdlc can't run yet", "builds or tests the work", "gh isn't on PATH", `remote "origin" isn't configured`} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr doesn't say %q:\n%s", want, got.stderr)
		}
	}
}

// loomlc won't run without its own commit policy checker: that is the check that says what a run may
// commit, and it is read from the checkout rather than from the workspace an agent can edit.
func TestRunRefusesACheckoutWithNoCommitPolicyChecker(t *testing.T) {
	files := checkoutFiles()
	delete(files, "scripts/commit-policy.sh")

	got := runIn(t, checkout(t, files), ready())

	if got.code != ExitFailure || !strings.Contains(got.stderr, "scripts/commit-policy.sh") {
		t.Fatalf("exit code = %d, stderr = %q; want it to name the checker it needs", got.code, got.stderr)
	}
}

func TestRunRejectsWhatCannotBeATask(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "two tasks", args: []string{"9", "10"}, want: "one task at a time"},
		{name: "an id with a flag in it", args: []string{"--add-label"}, want: "flag provided but not defined"},
		{name: "an id that isn't one", args: []string{"../../etc/passwd"}, want: "isn't a task id"},
		{name: "an id with a slash", args: []string{"9/1"}, want: "isn't a task id"},
		{name: "no passes", args: []string{"--max-iter", "-1"}, want: "--max-iter must be at least 1"},
		{name: "a configuration nobody named", args: []string{"--config", ""}, want: "--config needs a path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Main(context.Background(), append([]string{"run"}, tt.args...), Env{Stdout: &stdout, Stderr: &stderr})
			if code != ExitUsage {
				t.Errorf("exit code = %d, want %d", code, ExitUsage)
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("stderr = %q, want it to say %q", stderr.String(), tt.want)
			}
		})
	}
}

func TestRunHelpSaysWhatARunDoes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{"run", "--help"}, Env{Stdout: &stdout, Stderr: &stderr})

	if code != ExitOK || stderr.String() != "" {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{"loomlc run [flags] [task]", "the next ready one", "-max-iter", "-lifecycle"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help doesn't mention %q:\n%s", want, stdout.String())
		}
	}
}

// A configuration path is read from the checkout, like everything else a run reads, not from wherever
// loomlc happened to be started.
func TestRunReadsARelativeConfigFromTheCheckout(t *testing.T) {
	files := checkoutFiles()
	delete(files, "loomlc.yml")
	files["ops/loomlc.yml"] = runnable
	dir := checkout(t, files)

	got := runIn(t, dir, ready(), "--config", "ops/loomlc.yml")

	if got.code != ExitOK || !strings.Contains(got.stdout, "Nothing to do") {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", got.code, got.stdout, got.stderr)
	}
}

func TestRunNamesTheLifecyclesItHas(t *testing.T) {
	got := runIn(t, checkout(t, checkoutFiles()), ready(), "--lifecycle", "nope")

	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want a failure", got.code)
	}
	if !strings.Contains(got.stderr, `no lifecycle named "nope"`) || !strings.Contains(got.stderr, "sdlc") {
		t.Errorf("stderr = %q, want it to name the lifecycles there are", got.stderr)
	}
}
