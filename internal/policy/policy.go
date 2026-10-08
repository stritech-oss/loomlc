// Package policy runs loomlc's commit policy checker over a branch's commits and over the text it is
// about to post.
//
// The script comes from the operator's checkout, never from the workspace: a run can edit the workspace,
// and this is the check that says what a run may commit.
package policy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stritech-oss/loomlc/internal/proc"
)

// maxOutput bounds what a refusal quotes back.
const maxOutput = 8 << 10

// Checker runs the policy script.
type Checker struct {
	script string
	runner proc.Runner
	env    []string
}

// New returns a Checker that runs the script at path. env needs whatever the script needs, which is a
// shell, git, and the author identity the commits were made under.
func New(script string, runner proc.Runner, env []string) (*Checker, error) {
	if script == "" {
		return nil, fmt.Errorf("policy: no checker script")
	}
	abs, err := filepath.Abs(script)
	if err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	return &Checker{script: abs, runner: runner, env: env}, nil
}

// CheckRange checks every commit in base..head of the repository at dir.
func (c *Checker) CheckRange(ctx context.Context, dir, base, head string) error {
	return c.run(ctx, dir, "range", base, head)
}

// CheckText checks a pull request description or comment, which the policy holds to the same rules about
// agent attribution as a commit message.
func (c *Checker) CheckText(ctx context.Context, body string) error {
	file, err := os.CreateTemp("", "loomlc-text-*")
	if err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	defer func() { _ = os.Remove(file.Name()) }()

	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		return fmt.Errorf("policy: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	return c.run(ctx, "", "text", file.Name())
}

func (c *Checker) run(ctx context.Context, dir string, args ...string) error {
	var out bytes.Buffer
	res, err := c.runner.Run(ctx, proc.Cmd{
		Name:   "bash",
		Args:   append([]string{c.script}, args...),
		Dir:    dir,
		Env:    c.env,
		Stdout: &out,
		Stderr: &out,
	})
	switch {
	case err != nil:
		return fmt.Errorf("run the commit policy checker: %w", err)
	case res.ExitCode != 0:
		return fmt.Errorf("%s", bound(strings.TrimSpace(out.String())))
	}
	return nil
}

func bound(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + "\n…truncated by loomlc…"
}
