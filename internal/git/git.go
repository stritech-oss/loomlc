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

// New returns a Client that runs git through runner with env. GIT_TERMINAL_PROMPT=0 is added, so a missing
// credential fails instead of waiting for input that never comes.
func New(runner proc.Runner, env []string) *Client {
	return &Client{runner: runner, env: append(slices.Clone(env), "GIT_TERMINAL_PROMPT=0")}
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
