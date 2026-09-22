//go:build unix

package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as the command under test: when helperEnv is set, TestMain runs the helper
// named by its arguments instead of the tests. No external tools are started.
const helperEnv = "LOOMLC_PROC_HELPER"

// testGrace keeps cancellation tests fast while leaving the helper time to react to SIGTERM.
const testGrace = 200 * time.Millisecond

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(runHelper(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// helperEnviron is the environment every helper process gets. Under -race the test binary sleeps for a
// second when it exits so pending race reports can print; GORACE turns that off for helpers.
func helperEnviron() []string {
	return []string{helperEnv + "=1", "GORACE=atexit_sleep_ms=0"}
}

func runHelper(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "helper: missing mode")
		return 2
	}
	mode, rest := args[0], args[1:]
	switch mode {
	case "exit": // exit <code>: print to stdout and stderr, then exit with code
		code, err := strconv.Atoi(rest[0])
		if err != nil {
			return 2
		}
		if _, err := io.WriteString(os.Stdout, "out\n"); err != nil {
			return 1
		}
		if _, err := io.WriteString(os.Stderr, "err\n"); err != nil {
			return 1
		}
		return code
	case "cat": // copy stdin to stdout
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			return 1
		}
		return 0
	case "env": // print the environment, one entry per line
		for _, kv := range os.Environ() {
			fmt.Println(kv)
		}
		return 0
	case "pwd": // print the working directory
		wd, err := os.Getwd()
		if err != nil {
			return 1
		}
		fmt.Println(wd)
		return 0
	case "sleep": // sleep until killed
		time.Sleep(time.Hour)
		return 0
	case "ignore-term": // ignore-term <ready-file>: ignore SIGTERM, signal readiness, sleep until killed
		signal.Ignore(syscall.SIGTERM)
		return writeAndSleep(rest[0], "ready")
	case "spawn": // spawn <pid-file>: start a grandchild in this process group, record its pid, sleep
		child := exec.Command(os.Args[0], "sleep")
		child.Env = helperEnviron()
		if err := child.Start(); err != nil {
			return 1
		}
		return writeAndSleep(rest[0], strconv.Itoa(child.Process.Pid))
	case "spawn-exit": // spawn-exit <pid-file>: start a grandchild in this process group, record its pid, exit
		child := exec.Command(os.Args[0], "sleep")
		child.Env = helperEnviron()
		if err := child.Start(); err != nil {
			return 1
		}
		if err := os.WriteFile(rest[0], []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			return 1
		}
		return 0
	case "orphan": // orphan <pid-file>: start a grandchild in a new session that keeps stdout open, then exit
		child := exec.Command(os.Args[0], "sleep")
		child.Env = helperEnviron()
		child.Stdout = os.Stdout
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			return 1
		}
		if err := os.WriteFile(rest[0], []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "helper: unknown mode %q\n", mode)
		return 2
	}
}

// writeAndSleep writes content to path so the test knows the helper is ready, then sleeps until killed.
func writeAndSleep(path, content string) int {
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return 1
	}
	time.Sleep(time.Hour)
	return 0
}

// helper returns a Cmd that runs the named helper mode.
func helper(t *testing.T, args ...string) Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("find test binary: %v", err)
	}
	return Cmd{Name: exe, Args: args, Env: helperEnviron(), KillGrace: testGrace}
}

// waitFor polls cond until it holds. Process tests have to wait on real processes, so this polls with a
// deadline instead of sleeping for a fixed time.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func fileExists(path string) func() bool {
	return func() bool {
		_, err := os.Stat(path)
		return err == nil
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatalf("parse pid %q: %v", b, err)
	}
	return pid
}

func processGone(pid int) func() bool {
	return func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }
}

func TestRunReportsExitCodeAndOutput(t *testing.T) {
	for _, code := range []int{0, 3} {
		t.Run(fmt.Sprintf("exit %d", code), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			c := helper(t, "exit", strconv.Itoa(code))
			c.Stdout, c.Stderr = &stdout, &stderr

			res, err := Exec{}.Run(context.Background(), c)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.ExitCode != code {
				t.Errorf("ExitCode = %d, want %d", res.ExitCode, code)
			}
			if stdout.String() != "out\n" || stderr.String() != "err\n" {
				t.Errorf("stdout = %q, stderr = %q; want %q, %q", stdout.String(), stderr.String(), "out\n", "err\n")
			}
		})
	}
}

func TestRunPassesStdin(t *testing.T) {
	var stdout bytes.Buffer
	c := helper(t, "cat")
	c.Stdin = strings.NewReader("task text\n")
	c.Stdout = &stdout

	if _, err := (Exec{}).Run(context.Background(), c); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := stdout.String(); got != "task text\n" {
		t.Errorf("stdout = %q, want %q", got, "task text\n")
	}
}

func TestRunUsesOnlyTheGivenEnvironment(t *testing.T) {
	t.Setenv("LOOMLC_PROC_PARENT_ONLY", "must-not-leak")
	var stdout bytes.Buffer
	c := helper(t, "env")
	c.Env = append(c.Env, "GIVEN=yes")
	c.Stdout = &stdout

	if _, err := (Exec{}).Run(context.Background(), c); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.Fields(stdout.String())
	want := append(helperEnviron(), "GIVEN=yes")
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("environment = %q, want exactly %q", got, want)
	}
}

func TestRunUsesWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	var stdout bytes.Buffer
	c := helper(t, "pwd")
	c.Dir = dir
	c.Stdout = &stdout

	if _, err := (Exec{}).Run(context.Background(), c); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(stdout.String()))
	if err != nil {
		t.Fatalf("resolve reported dir: %v", err)
	}
	if got != want {
		t.Errorf("working directory = %q, want %q", got, want)
	}
}

func TestRunRejectsInvalidCommands(t *testing.T) {
	tests := []struct {
		name    string
		cmd     Cmd
		wantErr string
	}{
		{name: "empty name", cmd: Cmd{}, wantErr: "empty command name"},
		{name: "missing executable", cmd: Cmd{Name: "loomlc-no-such-command"}, wantErr: "start loomlc-no-such-command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Exec{}.Run(context.Background(), tt.cmd)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunDoesNotStartWhenContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Exec{}.Run(ctx, helper(t, "sleep"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRunStopsCommandWhenDeadlinePasses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	res, err := Exec{}.Run(ctx, helper(t, "sleep"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if res.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 for a signaled process", res.ExitCode)
	}
}

func TestRunStopsProcessesTheCommandStarted(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errs := make(chan error, 1)
	go func() {
		_, err := Exec{}.Run(ctx, helper(t, "spawn", pidFile))
		errs <- err
	}()
	waitFor(t, "grandchild to start", fileExists(pidFile))
	grandchild := readPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })

	cancel()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	waitFor(t, "grandchild to be stopped", processGone(grandchild))
}

// A command that exits cleanly can still leave something running behind it — an agent that starts a dev
// server, say. Reporting success and walking away would leave it there for good.
func TestRunStopsProcessesLeftBehindByACommandThatExited(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	res, err := Exec{}.Run(context.Background(), helper(t, "spawn-exit", pidFile))
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Run = %+v, %v; want a clean exit", res, err)
	}
	grandchild := readPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })

	waitFor(t, "the process left behind to be stopped", processGone(grandchild))
}

// A command that never ran has no exit status, and reporting 0 would read as success.
func TestRunReportsNoExitStatusWhenTheCommandNeverRan(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		ctx  context.Context
		cmd  Cmd
	}{
		{name: "executable not found", ctx: context.Background(), cmd: Cmd{Name: filepath.Join(t.TempDir(), "nope"), Env: []string{}}},
		{name: "context already done", ctx: canceled, cmd: helper(t, "exit", "0")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Exec{}.Run(tt.ctx, tt.cmd)
			if err == nil {
				t.Fatal("Run succeeded, want an error")
			}
			if res.ExitCode != -1 {
				t.Errorf("exit code = %d, want -1: the command never ran", res.ExitCode)
			}
		})
	}
}

func TestRunKillsCommandThatIgnoresTerminate(t *testing.T) {
	readyFile := filepath.Join(t.TempDir(), "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type outcome struct {
		res Result
		err error
	}
	outcomes := make(chan outcome, 1)
	go func() {
		res, err := Exec{}.Run(ctx, helper(t, "ignore-term", readyFile))
		outcomes <- outcome{res, err}
	}()
	waitFor(t, "helper to ignore SIGTERM", fileExists(readyFile))

	canceled := time.Now()
	cancel()
	got := <-outcomes

	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", got.err)
	}
	if got.res.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 for a killed process", got.res.ExitCode)
	}
	if elapsed := time.Since(canceled); elapsed < testGrace {
		t.Errorf("Run returned %v after cancel, before the %v grace period; SIGKILL came too early", elapsed, testGrace)
	}
}

func TestRunBoundsWaitWhenOutputIsLeftOpen(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "orphan.pid")
	var stdout bytes.Buffer
	c := helper(t, "orphan", pidFile)
	c.Stdout = &stdout

	res, err := Exec{}.Run(context.Background(), c)
	if pid, readErr := os.ReadFile(pidFile); readErr == nil {
		if orphan, convErr := strconv.Atoi(string(pid)); convErr == nil {
			t.Cleanup(func() { _ = syscall.Kill(orphan, syscall.SIGKILL) })
		}
	}

	if !errors.Is(err, ErrOutputLeftOpen) {
		t.Fatalf("error = %v, want ErrOutputLeftOpen", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestLookPath(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("find test binary: %v", err)
	}

	got, err := Exec{}.LookPath(exe)
	if err != nil || got != exe {
		t.Errorf("LookPath(%q) = %q, %v; want %q, nil", exe, got, err, exe)
	}

	if _, err := (Exec{}).LookPath("loomlc-no-such-command"); !errors.Is(err, ErrNotFound) {
		t.Errorf("LookPath of a missing command: error = %v, want ErrNotFound", err)
	}
}
