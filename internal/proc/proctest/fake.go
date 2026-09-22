// Package proctest provides a fake proc.Runner that records commands and replays canned responses, so
// code that runs commands can be tested without starting processes.
package proctest

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/stritech-oss/loomlc/internal/proc"
)

// Response is what the fake returns for a matching command.
type Response struct {
	Stdout, Stderr string
	ExitCode       int
	// Err is returned from Run after the output is written. Use it to simulate a command that can't
	// start, or one that was canceled.
	Err error
}

// Call records one command the fake was asked to run.
type Call struct {
	Cmd proc.Cmd
	// Stdin is everything read from Cmd.Stdin.
	Stdin string
}

// Argv returns the command's name followed by its arguments.
func (c Call) Argv() []string {
	return append([]string{c.Cmd.Name}, c.Cmd.Args...)
}

// Fake is a proc.Runner for tests. The zero value has no responses and finds no executables. It is safe
// for concurrent use.
type Fake struct {
	mu        sync.Mutex
	responses []prefixResponse
	paths     map[string]string
	calls     []Call
}

type prefixResponse struct {
	prefix []string
	resp   Response
}

var _ proc.Runner = (*Fake)(nil)

// On makes commands whose argv (name, then arguments) starts with prefix return resp. When several
// prefixes match, the longest wins; among equal lengths, the one registered last wins.
func (f *Fake) On(resp Response, prefix ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses = append(f.responses, prefixResponse{prefix: slices.Clone(prefix), resp: resp})
}

// SetPath makes LookPath report path for name.
func (f *Fake) SetPath(name, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.paths == nil {
		f.paths = map[string]string{}
	}
	f.paths[name] = path
}

// Run implements proc.Runner. It records c, then writes the matching response's output and returns its
// exit code and error. A command with no matching response is an error, so tests fail loudly on
// commands they didn't expect.
func (f *Fake) Run(ctx context.Context, c proc.Cmd) (proc.Result, error) {
	// Checked before anything is recorded, because proc.Exec refuses a done context before it starts a
	// process: a fake that records the call would let a test pass against behaviour that can't happen.
	if err := ctx.Err(); err != nil {
		return proc.Result{ExitCode: -1}, fmt.Errorf("proctest: run %s: %w", c.Name, err)
	}

	var stdin string
	if c.Stdin != nil {
		b, err := io.ReadAll(c.Stdin)
		if err != nil {
			return proc.Result{}, fmt.Errorf("proctest: read stdin of %s: %w", c.Name, err)
		}
		stdin = string(b)
	}

	call := Call{Cmd: c, Stdin: stdin}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	resp, ok := f.match(call.Argv())
	f.mu.Unlock()

	if !ok {
		return proc.Result{ExitCode: -1}, fmt.Errorf("proctest: no response registered for %q", call.Argv())
	}
	if err := write(c.Stdout, resp.Stdout); err != nil {
		return proc.Result{}, fmt.Errorf("proctest: write stdout of %s: %w", c.Name, err)
	}
	if err := write(c.Stderr, resp.Stderr); err != nil {
		return proc.Result{}, fmt.Errorf("proctest: write stderr of %s: %w", c.Name, err)
	}
	return proc.Result{ExitCode: resp.ExitCode}, resp.Err
}

// LookPath implements proc.Runner using the paths set with SetPath.
func (f *Fake) LookPath(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if path, ok := f.paths[name]; ok {
		return path, nil
	}
	return "", fmt.Errorf("proctest: look up %s: %w", name, proc.ErrNotFound)
}

// Calls returns the commands run so far, in order.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// match returns the response with the longest prefix of argv. The caller holds f.mu.
func (f *Fake) match(argv []string) (Response, bool) {
	best := -1
	for i, r := range f.responses {
		if len(r.prefix) > len(argv) || !slices.Equal(r.prefix, argv[:len(r.prefix)]) {
			continue
		}
		if best < 0 || len(r.prefix) >= len(f.responses[best].prefix) {
			best = i
		}
	}
	if best < 0 {
		return Response{}, false
	}
	return f.responses[best].resp, true
}

func write(w io.Writer, s string) error {
	if w == nil || s == "" {
		return nil
	}
	_, err := io.WriteString(w, s)
	return err
}
