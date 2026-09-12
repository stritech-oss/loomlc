package proctest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stritech-oss/loomlc/internal/proc"
)

func TestFakeReplaysLongestMatchingPrefix(t *testing.T) {
	var f Fake
	f.On(Response{Stdout: "any git\n"}, "git")
	f.On(Response{Stdout: "status\n", ExitCode: 1}, "git", "status")
	f.On(Response{Stdout: "later git\n"}, "git")

	tests := []struct {
		name       string
		argv       []string
		wantStdout string
		wantCode   int
	}{
		{name: "longer prefix wins", argv: []string{"git", "status", "--short"}, wantStdout: "status\n", wantCode: 1},
		{name: "equal prefixes: last registered wins", argv: []string{"git", "log"}, wantStdout: "later git\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			res, err := f.Run(context.Background(), proc.Cmd{Name: tt.argv[0], Args: tt.argv[1:], Stdout: &stdout})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if stdout.String() != tt.wantStdout || res.ExitCode != tt.wantCode {
				t.Errorf("stdout = %q, exit = %d; want %q, %d", stdout.String(), res.ExitCode, tt.wantStdout, tt.wantCode)
			}
		})
	}
}

func TestFakeRejectsUnexpectedCommands(t *testing.T) {
	var f Fake
	f.On(Response{}, "git", "status")

	_, err := f.Run(context.Background(), proc.Cmd{Name: "git"})
	if err == nil || !strings.Contains(err.Error(), "no response registered") {
		t.Fatalf("error = %v, want a no-response error", err)
	}
}

func TestFakeRecordsCallsAndStdin(t *testing.T) {
	var f Fake
	f.On(Response{}, "claude")

	c := proc.Cmd{Name: "claude", Args: []string{"-p"}, Dir: "/work", Stdin: strings.NewReader("plan the task")}
	if _, err := f.Run(context.Background(), c); err != nil {
		t.Fatalf("Run: %v", err)
	}

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(calls))
	}
	got := calls[0]
	if strings.Join(got.Argv(), " ") != "claude -p" || got.Cmd.Dir != "/work" || got.Stdin != "plan the task" {
		t.Errorf("call = argv %q, dir %q, stdin %q", got.Argv(), got.Cmd.Dir, got.Stdin)
	}
}

func TestFakeWritesStderrAndReturnsResponseError(t *testing.T) {
	var f Fake
	startErr := errors.New("cannot start")
	f.On(Response{Stderr: "boom\n", ExitCode: 2, Err: startErr}, "tool")

	var stderr bytes.Buffer
	res, err := f.Run(context.Background(), proc.Cmd{Name: "tool", Stderr: &stderr})
	if !errors.Is(err, startErr) {
		t.Errorf("error = %v, want %v", err, startErr)
	}
	if res.ExitCode != 2 || stderr.String() != "boom\n" {
		t.Errorf("exit = %d, stderr = %q; want 2, %q", res.ExitCode, stderr.String(), "boom\n")
	}
}

func TestFakeHonorsCanceledContext(t *testing.T) {
	var f Fake
	f.On(Response{Stdout: "ignored"}, "tool")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.Run(ctx, proc.Cmd{Name: "tool"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestFakeLookPath(t *testing.T) {
	var f Fake
	f.SetPath("claude", "/usr/local/bin/claude")

	if got, err := f.LookPath("claude"); err != nil || got != "/usr/local/bin/claude" {
		t.Errorf("LookPath(claude) = %q, %v; want /usr/local/bin/claude, nil", got, err)
	}
	if _, err := f.LookPath("codex"); !errors.Is(err, proc.ErrNotFound) {
		t.Errorf("LookPath(codex): error = %v, want proc.ErrNotFound", err)
	}
}

func TestFakeIsSafeForConcurrentUse(t *testing.T) {
	var f Fake
	f.On(Response{Stdout: "ok"}, "tool")

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if _, err := f.Run(context.Background(), proc.Cmd{Name: "tool", Args: []string{fmt.Sprint(i)}}); err != nil {
				t.Errorf("Run: %v", err)
			}
		})
	}
	wg.Wait()

	if n := len(f.Calls()); n != 20 {
		t.Errorf("recorded %d calls, want 20", n)
	}
}
