// Package gate runs a lifecycle's checks — build, lint, tests — in a workspace. Agents can't run them,
// so loomlc runs them between steps and puts the results in the next prompt.
package gate

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/stritech-oss/loomlc/internal/proc"
	"github.com/stritech-oss/loomlc/internal/redact"
)

const (
	// defaultTimeout bounds one check. A build that hangs mustn't hang the run.
	defaultTimeout = 30 * time.Minute
	// maxOutput bounds what a check contributes to a prompt.
	maxOutput = 16 << 10
	truncated = "\n…truncated by loomlc…\n"
)

// Result is what one check did.
type Result struct {
	Name string
	// Passed is whether the command exited zero.
	Passed bool
	// Output is the end of what it printed, redacted and bounded.
	Output string
}

// Options configure a Runner.
type Options struct {
	// Env is the checks' complete environment. They run the repository's own tooling, so this needs
	// whatever that tooling needs.
	Env []string
	// Redact masks secrets in output, which goes into prompts and comments. Nil masks nothing.
	Redact *redact.Redactor
	// Timeout bounds one check. Zero means 30 minutes.
	Timeout time.Duration
}

// Runner runs checks.
type Runner struct {
	opts   Options
	runner proc.Runner
}

// New returns a Runner that runs checks through runner.
func New(opts Options, runner proc.Runner) *Runner {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	return &Runner{opts: opts, runner: runner}
}

// Run runs each command in dir, in order, and reports how each one went. A command that can't start is
// an error: the lifecycle names a check this machine doesn't have, which an operator fixes.
func (r *Runner) Run(ctx context.Context, dir string, commands [][]string) ([]Result, error) {
	var results []Result
	for _, argv := range commands {
		if len(argv) == 0 || argv[0] == "" {
			return nil, fmt.Errorf("run checks: a check has no command")
		}
		result, err := r.one(ctx, dir, argv)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (r *Runner) one(ctx context.Context, dir string, argv []string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()

	name := strings.Join(argv, " ")
	var out bytes.Buffer
	res, err := r.runner.Run(ctx, proc.Cmd{
		Name:   argv[0],
		Args:   argv[1:],
		Dir:    dir,
		Env:    r.opts.Env,
		Stdout: &out,
		Stderr: &out,
	})
	switch {
	case ctx.Err() != nil:
		// A check that ran out of time is a failed check, not a broken lifecycle.
		return Result{Name: name, Output: r.mask(out.String()) + fmt.Sprintf("\nloomlc stopped this check after %s.\n", r.opts.Timeout)}, nil
	case err != nil:
		return Result{}, fmt.Errorf("run check %q: %w", name, err)
	}
	return Result{Name: name, Passed: res.ExitCode == 0, Output: r.mask(out.String())}, nil
}

// mask redacts a check's output and keeps its end, where a failure explains itself.
func (r *Runner) mask(output string) string {
	if r.opts.Redact != nil {
		output = r.opts.Redact.String(output)
	}
	if len(output) <= maxOutput {
		return output
	}
	cut := len(output) - maxOutput
	for cut < len(output) && !utf8Start(output[cut]) {
		cut++
	}
	return truncated + output[cut:]
}

// utf8Start reports whether b begins a character, so a cut doesn't split one.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// Failed returns the checks that didn't pass.
func Failed(results []Result) []Result {
	var failed []Result
	for _, r := range results {
		if !r.Passed {
			failed = append(failed, r)
		}
	}
	return failed
}
