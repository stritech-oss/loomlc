package git

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/proc/proctest"
)

func TestRunReturnsOutputWithoutTrailingNewline(t *testing.T) {
	var fake proctest.Fake
	fake.On(proctest.Response{Stdout: "feat/issue-42\n"}, "git", "rev-parse", "--abbrev-ref", "HEAD")
	c := New(&fake, []string{"HOME=/home/user"})

	got, err := c.Run(context.Background(), "/work/issue-42", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || got != "feat/issue-42" {
		t.Fatalf("Run = %q, %v; want %q, nil", got, err, "feat/issue-42")
	}

	call := fake.Calls()[0]
	if call.Cmd.Dir != "/work/issue-42" {
		t.Errorf("dir = %q, want /work/issue-42", call.Cmd.Dir)
	}
	if want := "HOME=/home/user GIT_TERMINAL_PROMPT=0"; strings.Join(call.Cmd.Env, " ") != want {
		t.Errorf("env = %q, want %q", call.Cmd.Env, want)
	}
}

func TestRunReportsFailedCommands(t *testing.T) {
	var fake proctest.Fake
	fake.On(proctest.Response{Stderr: "fatal: couldn't find remote ref feat/missing\n", ExitCode: 128}, "git", "fetch")
	c := New(&fake, nil)

	_, err := c.Run(context.Background(), "/repo", "fetch", "origin", "feat/missing")
	var gerr *Error
	if !errors.As(err, &gerr) {
		t.Fatalf("error = %v, want a *git.Error", err)
	}
	if gerr.ExitCode != 128 || gerr.Stderr != "fatal: couldn't find remote ref feat/missing" {
		t.Errorf("error = %+v", gerr)
	}
	if want := "git fetch origin feat/missing: exit status 128: fatal: couldn't find remote ref feat/missing"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestRunWrapsRunnerErrors(t *testing.T) {
	c := New(&proctest.Fake{}, nil) // no responses: every command fails to run

	_, err := c.Run(context.Background(), "/repo", "status")
	var gerr *Error
	if err == nil || errors.As(err, &gerr) || !strings.HasPrefix(err.Error(), "git status: ") {
		t.Errorf("error = %v, want a wrapped runner error", err)
	}
}

func TestNewDoesNotModifyTheCallersEnvironment(t *testing.T) {
	env := make([]string, 1, 2)
	env[0] = "HOME=/home/user"
	New(&proctest.Fake{}, env)
	if got := env[:cap(env)][1]; got != "" {
		t.Errorf("New wrote %q into the caller's slice", got)
	}
}

// GIT_DIR and its relatives outrank the directory a command runs in, and git sets them for hooks,
// `git rebase --exec`, `git bisect run`, and several CI checkout actions. A caller that passes its own
// environment through — the documented way to reach a remote — would otherwise have every command act on
// whatever repository those name.
func TestNewDropsVariablesThatRedirectTheRepository(t *testing.T) {
	var fake proctest.Fake
	fake.On(proctest.Response{}, "git")
	env := []string{
		"HOME=/home/user",
		"GIT_DIR=/elsewhere/.git",
		"GIT_WORK_TREE=/elsewhere",
		"GIT_INDEX_FILE=/elsewhere/.git/index",
		"GIT_OBJECT_DIRECTORY=/elsewhere/.git/objects",
		"GIT_NAMESPACE=other",
		"GIT_SSH_COMMAND=ssh -i key",
	}

	if _, err := New(&fake, env).Run(context.Background(), "/repo", "status"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := strings.Join(fake.Calls()[0].Cmd.Env, " ")
	for _, gone := range []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_OBJECT_DIRECTORY=", "GIT_NAMESPACE="} {
		if strings.Contains(got, gone) {
			t.Errorf("env still has %s; the command would act on another repository", gone)
		}
	}
	// Everything else the caller passed is still needed to reach the remote.
	for _, kept := range []string{"HOME=/home/user", "GIT_SSH_COMMAND=ssh -i key", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(got, kept) {
			t.Errorf("env = %q, want it to keep %q", got, kept)
		}
	}
}
