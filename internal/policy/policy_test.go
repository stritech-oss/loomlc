package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/proc"
	"github.com/stritech-oss/loomlc/internal/proc/proctest"
)

// script is the repository's own checker, which is what a run uses.
func script(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "scripts", "commit-policy.sh")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no policy script: %v", err)
	}
	return path
}

func checker(t *testing.T) *Checker {
	t.Helper()
	runner := proc.Exec{}
	if _, err := runner.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}
	c, err := New(script(t), runner, []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_AUTHOR_NAME=Ada Human",
		"GIT_AUTHOR_EMAIL=ada@example.com",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestCheckTextRunsTheRealScript(t *testing.T) {
	c := checker(t)
	ctx := context.Background()

	if err := c.CheckText(ctx, "## Done\n\nPrints the build commit.\n\n## Issue / Card\n\nCloses #9\n"); err != nil {
		t.Errorf("a clean description was refused: %v", err)
	}

	err := c.CheckText(ctx, "## Done\n\nPrints it.\n\nCo-authored-by: Claude <noreply@anthropic.com>\n")
	if err == nil {
		t.Fatal("a description with attribution was accepted")
	}
	if !strings.Contains(err.Error(), "attribution") {
		t.Errorf("error = %v, want it to quote the script", err)
	}
}

func TestCheckRangeRunsInTheWorkspace(t *testing.T) {
	var fake proctest.Fake
	fake.On(proctest.Response{}, "bash")
	c, err := New(script(t), &fake, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := c.CheckRange(context.Background(), "/work/issue-9", "abc1234", "HEAD"); err != nil {
		t.Fatalf("CheckRange: %v", err)
	}
	call := fake.Calls()[0]
	if call.Cmd.Dir != "/work/issue-9" {
		t.Errorf("ran in %q, want the workspace: the commits are there", call.Cmd.Dir)
	}
	args := strings.Join(call.Cmd.Args, " ")
	if !strings.HasSuffix(args, "range abc1234 HEAD") {
		t.Errorf("args = %q", args)
	}
	// The script is the operator's copy, not whatever the workspace holds.
	if strings.HasPrefix(call.Cmd.Args[0], "/work/") || !filepath.IsAbs(call.Cmd.Args[0]) {
		t.Errorf("script = %q, want an absolute path outside the workspace", call.Cmd.Args[0])
	}
}

func TestCheckRangeReportsWhatTheScriptSaid(t *testing.T) {
	var fake proctest.Fake
	fake.On(proctest.Response{Stderr: `commit-policy: 1234567: missing Signed-off-by`, ExitCode: 1}, "bash")
	c, err := New(script(t), &fake, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = c.CheckRange(context.Background(), "/work", "abc", "HEAD")
	if err == nil || !strings.Contains(err.Error(), "missing Signed-off-by") {
		t.Errorf("error = %v, want the script's own words", err)
	}
}

func TestNewRefusesAScriptItCannotFind(t *testing.T) {
	if _, err := New("", proc.Exec{}, nil); err == nil {
		t.Error("an empty path was accepted")
	}
	if _, err := New(filepath.Join(t.TempDir(), "nope.sh"), proc.Exec{}, nil); err == nil {
		t.Error("a missing script was accepted")
	}
}
