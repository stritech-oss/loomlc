package gate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stritech-oss/loomlc/internal/proc/proctest"
	"github.com/stritech-oss/loomlc/internal/redact"
)

func TestRunReportsEachCheck(t *testing.T) {
	var fake proctest.Fake
	fake.On(proctest.Response{Stdout: "ok\n"}, "task", "lint")
	fake.On(proctest.Response{Stdout: "--- FAIL: TestVersion\n", ExitCode: 1}, "task", "test")

	results, err := New(Options{}, &fake).Run(context.Background(), "/work", [][]string{{"task", "lint"}, {"task", "test"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want one per check", results)
	}
	if results[0].Name != "task lint" || !results[0].Passed {
		t.Errorf("first = %+v", results[0])
	}
	if results[1].Passed || !strings.Contains(results[1].Output, "--- FAIL") {
		t.Errorf("second = %+v", results[1])
	}
	if got := strings.Join(fake.Calls()[0].Argv(), " "); got != "task lint" {
		t.Errorf("first command = %q", got)
	}
	if fake.Calls()[0].Cmd.Dir != "/work" {
		t.Errorf("checks ran in %q, want the workspace", fake.Calls()[0].Cmd.Dir)
	}
}

// A check's output goes into a prompt and into comments, so its secrets are masked first.
func TestRunMasksSecretsInOutput(t *testing.T) {
	const secret = "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var fake proctest.Fake
	fake.On(proctest.Response{Stdout: "using token " + secret + "\n", ExitCode: 1}, "task", "test")

	results, err := New(Options{Redact: redact.New(secret)}, &fake).Run(context.Background(), "/work", [][]string{{"task", "test"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(results[0].Output, secret) {
		t.Errorf("output = %q, want the token masked", results[0].Output)
	}
	if !strings.Contains(results[0].Output, redact.Mask) {
		t.Errorf("output = %q, want a mask", results[0].Output)
	}
}

// Long output is cut from the front: a failure explains itself at the end.
func TestRunKeepsTheEndOfLongOutput(t *testing.T) {
	var fake proctest.Fake
	fake.On(proctest.Response{Stdout: strings.Repeat("noise\n", 8000) + "the error is here\n", ExitCode: 1}, "task", "test")

	results, err := New(Options{}, &fake).Run(context.Background(), "/work", [][]string{{"task", "test"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(results[0].Output, "the error is here") {
		t.Error("the end of the output was cut away")
	}
	if len(results[0].Output) > maxOutput+len(truncated) {
		t.Errorf("output is %d bytes, want it bounded", len(results[0].Output))
	}
	if !strings.Contains(results[0].Output, "truncated") {
		t.Error("the output was cut without saying so")
	}
}

// A check this machine can't run is the lifecycle naming something that isn't there, which an operator
// fixes — not a finding for an agent.
func TestRunFailsWhenACheckCannotStart(t *testing.T) {
	var fake proctest.Fake
	fake.On(proctest.Response{Err: context.DeadlineExceeded}, "task", "test")

	if _, err := New(Options{}, &fake).Run(context.Background(), "/work", [][]string{{"task", "test"}}); err == nil {
		t.Error("Run succeeded with a check that couldn't start")
	}
	if _, err := New(Options{}, &fake).Run(context.Background(), "/work", [][]string{{}}); err == nil {
		t.Error("Run accepted a check with no command")
	}
}

func TestRunStopsACheckThatRunsTooLong(t *testing.T) {
	var fake proctest.Fake
	fake.On(proctest.Response{Err: context.DeadlineExceeded}, "task", "test")

	results, err := New(Options{Timeout: time.Nanosecond}, &fake).Run(context.Background(), "/work", [][]string{{"task", "test"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Passed {
		t.Fatalf("results = %+v, want one failed check", results)
	}
	if !strings.Contains(results[0].Output, "stopped this check") {
		t.Errorf("output = %q, want it to say the check ran out of time", results[0].Output)
	}
}

func TestFailedPicksOutTheFailures(t *testing.T) {
	results := []Result{{Name: "a", Passed: true}, {Name: "b"}, {Name: "c", Passed: true}}
	failed := Failed(results)
	if len(failed) != 1 || failed[0].Name != "b" {
		t.Errorf("Failed = %+v, want only b", failed)
	}
	if Failed(nil) != nil {
		t.Error("Failed(nil) isn't nil")
	}
}
