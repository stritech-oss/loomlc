// Package proc runs external commands by argument vector, with an explicit environment. Every process
// the command started is stopped when it ends, whether it was canceled or exited on its own.
//
// It is the only package in loomlc that imports os/exec (docs/conventions.md). Everything else runs
// commands through Runner, which tests replace with proctest.Fake.
package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// DefaultKillGrace is how long a canceled command has to exit after SIGTERM before it is killed.
const DefaultKillGrace = 10 * time.Second

// ErrNotFound means an executable wasn't found in PATH.
var ErrNotFound = exec.ErrNotFound

// ErrOutputLeftOpen means the command exited but a process it started kept its output open, so the
// captured output may be incomplete.
var ErrOutputLeftOpen = errors.New("command exited but another process kept its output open")

// Cmd describes a command to run.
type Cmd struct {
	// Name is the executable: a name looked up in loomlc's own PATH, or a path.
	Name string
	// Args are the arguments after Name, passed to the executable as-is. No shell is involved.
	Args []string
	// Dir is the working directory. Empty means loomlc's current directory.
	Dir string
	// Env is the complete environment, as KEY=value entries. Nil means an empty environment: nothing
	// is inherited from loomlc unless the caller copies it in.
	Env []string
	// Stdin is the command's input. Nil means no input.
	Stdin io.Reader
	// Stdout and Stderr receive the command's output. Nil discards it.
	Stdout, Stderr io.Writer
	// KillGrace is how long the command has to exit after SIGTERM once ctx ends, before it and every
	// process in its group are killed. It also bounds how long Run waits for output after the command
	// exits. Zero means DefaultKillGrace.
	KillGrace time.Duration
}

// Result reports how a command ended.
type Result struct {
	// ExitCode is the command's exit status, or -1 if it was ended by a signal.
	ExitCode int
}

// Runner runs commands.
type Runner interface {
	// Run runs c and waits for it to finish. A non-zero exit status is reported in Result, not as an
	// error. The error is non-nil when the command can't be started, when ctx ends before the command
	// does (it wraps ctx.Err()), or when the command exits but leaves its output open
	// (ErrOutputLeftOpen).
	Run(ctx context.Context, c Cmd) (Result, error)
	// LookPath reports where the named executable is found. A missing executable wraps ErrNotFound.
	LookPath(name string) (string, error)
}

// Exec runs commands as child processes.
type Exec struct{}

var _ Runner = Exec{}

// Run implements Runner.
func (Exec) Run(ctx context.Context, c Cmd) (Result, error) {
	if c.Name == "" {
		return Result{}, errors.New("run command: empty command name")
	}
	if err := ctx.Err(); err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("run %s: %w", c.Name, err)
	}

	grace := c.KillGrace
	if grace <= 0 {
		grace = DefaultKillGrace
	}

	cmd := exec.Command(c.Name, c.Args...) //nolint:gosec // G204: running caller-chosen commands is this package's purpose, and argv never reaches a shell.
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	if cmd.Env == nil {
		cmd.Env = []string{} // exec.Cmd inherits loomlc's environment when Env is nil.
	}
	cmd.Stdin = c.Stdin
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	cmd.WaitDelay = grace
	startInOwnGroup(cmd)

	if err := cmd.Start(); err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("start %s: %w", c.Name, err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		// The command is finished, but anything it started in the background isn't. Leaving those
		// running would outlive the run that asked for them, so the group goes with the command.
		cleanUpGroup(cmd.Process, grace)
		return finish(c.Name, cmd, err)
	case <-ctx.Done():
	}

	// The command may have exited in the same instant the context ended. Signalling a group whose
	// leader has already been reaped can reach an unrelated group once the id is reused.
	select {
	case err := <-done:
		cleanUpGroup(cmd.Process, grace)
		res, _ := finish(c.Name, cmd, err)
		return res, fmt.Errorf("run %s: %w", c.Name, ctx.Err())
	default:
	}

	terminateGroup(cmd.Process)
	timer := time.NewTimer(grace)
	defer timer.Stop()

	var waitErr error
	select {
	case waitErr = <-done:
	case <-timer.C:
		killGroup(cmd.Process)
		// A process that can't be killed — stuck in uninterruptible I/O on a dead mount, say — must
		// not hang the run as well. Give up after the same grace and say so.
		giveUp := time.NewTimer(grace)
		defer giveUp.Stop()
		select {
		case waitErr = <-done:
		case <-giveUp.C:
			return Result{ExitCode: -1}, fmt.Errorf("run %s: %w; it didn't exit after SIGKILL", c.Name, ctx.Err())
		}
	}
	cleanUpGroup(cmd.Process, grace)
	res, _ := finish(c.Name, cmd, waitErr) // the context error explains the outcome better than a wait error
	return res, fmt.Errorf("run %s: %w", c.Name, ctx.Err())
}

// cleanUpGroup stops anything the command left running in its process group. Nothing is left alive in
// the common case, so this costs a single signal that reports "no such group".
func cleanUpGroup(p *os.Process, grace time.Duration) {
	if p == nil || !groupAlive(p) {
		return
	}
	terminateGroup(p)

	const poll = 20 * time.Millisecond
	for waited := time.Duration(0); waited < grace; waited += poll {
		if !groupAlive(p) {
			return
		}
		time.Sleep(poll)
	}
	killGroup(p)
}

// LookPath implements Runner. It searches loomlc's own PATH, the same way Run resolves Cmd.Name.
func (Exec) LookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("look up %s: %w", name, err)
	}
	return path, nil
}

// finish turns the result of Wait into a Result and the error Run reports.
func finish(name string, cmd *exec.Cmd, waitErr error) (Result, error) {
	res := Result{ExitCode: -1}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}

	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
		return res, nil
	case errors.Is(waitErr, exec.ErrWaitDelay):
		return res, fmt.Errorf("run %s: %w", name, ErrOutputLeftOpen)
	case errors.As(waitErr, &exitErr):
		return res, nil
	default:
		return res, fmt.Errorf("wait for %s: %w", name, waitErr)
	}
}
