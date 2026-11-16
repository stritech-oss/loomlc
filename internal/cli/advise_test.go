package cli

import (
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/proc/proctest"
)

// A reviewer who is the operator is said before the run, and the run goes on: the request is only
// dropped once the pull request is open.
func TestRunWarnsWhenTheReviewerIsTheOperator(t *testing.T) {
	files := checkoutFiles()
	files["loomlc.yml"] = runnable + "sinks:\n  github-pr:\n    reviewer: pedoch\n"
	fake := ready()
	fake.On(proctest.Response{Stdout: `{"login":"pedoch"}`}, "gh", "api", "user")

	got := runIn(t, checkout(t, files), fake)

	if got.code != ExitOK {
		t.Fatalf("exit code = %d, stderr = %q", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "warning: sinks.github-pr.reviewer") {
		t.Errorf("stderr = %q, want the warning", got.stderr)
	}
	if !strings.Contains(got.stdout, "Nothing to do") {
		t.Errorf("stdout = %q, want the run to go on", got.stdout)
	}
}
