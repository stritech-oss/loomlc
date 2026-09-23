// Package git runs git commands through internal/proc.
package git

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/stritech-oss/loomlc/internal/proc"
)

// maxStderr bounds how much of git's error output an Error keeps.
const maxStderr = 4 << 10

// Client runs git.
type Client struct {
	runner proc.Runner
	env    []string
}

// repoVars name the repository a git command acts on, and they outrank the directory it runs in. A
// caller passing its own environment through — which is the documented way to reach a remote — can be
// running under one of these without knowing: git sets them for hooks, `git rebase --exec`, `git bisect
// run`, and several CI checkout actions. Left in place, every command would act on that repository
// instead of the one loomlc was pointed at.
var repoVars = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_NAMESPACE",
	"GIT_PREFIX",
	"GIT_CEILING_DIRECTORIES",
}

// New returns a Client that runs git through runner with env. Variables that would redirect a command to
// another repository are dropped, and GIT_TERMINAL_PROMPT=0 is added so a missing credential fails
// instead of waiting for input that never comes.
func New(runner proc.Runner, env []string) *Client {
	kept := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); !ok || !slices.Contains(repoVars, name) {
			kept = append(kept, kv)
		}
	}
	return &Client{runner: runner, env: append(kept, "GIT_TERMINAL_PROMPT=0")}
}

// Error is a git command that exited unsuccessfully.
type Error struct {
	Args     []string
	ExitCode int
	// Stderr is the end of git's error output.
	Stderr string
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("git %s: exit status %d", strings.Join(e.Args, " "), e.ExitCode)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

// Run runs git with args in dir and returns its standard output without the trailing newline.
func (c *Client) Run(ctx context.Context, dir string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	res, err := c.runner.Run(ctx, proc.Cmd{Name: "git", Args: args, Dir: dir, Env: c.env, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	if res.ExitCode != 0 {
		return "", &Error{Args: args, ExitCode: res.ExitCode, Stderr: tail(stderr.String())}
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxStderr {
		s = "…" + s[len(s)-maxStderr:]
	}
	return s
}
