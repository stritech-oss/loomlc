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
