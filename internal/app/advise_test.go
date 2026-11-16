package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/proc/proctest"
)

const reviewerYAML = readyYAML + "sinks:\n  github-pr:\n    reviewer: Pedoch\n"

func adviceFor(t *testing.T, yaml string, fake func(root string) *proctest.Fake) ([]string, *proctest.Fake) {
	t.Helper()
	dir := repo(t)
	writeTemplate(t, dir)
	f := fake(dir)
	a := newApp(t, cfg(t, yaml), dir, f)
	lc, err := a.Lifecycle("")
	if err != nil {
		t.Fatalf("Lifecycle: %v", err)
	}
	return a.Advise(context.Background(), lc), f
}

func whoIs(login string) func(string) *proctest.Fake {
	return func(root string) *proctest.Fake {
		f := ready(root)
		f.On(proctest.Response{Stdout: `{"login":"` + login + `"}`}, "gh", "api", "user")
		return f
	}
}

// GitHub drops a review request aimed at the pull request's author, and loomlc opens it as the account
// gh is signed in to, so a reviewer who is that account is worth saying before anything runs.
func TestAdviseWarnsWhenTheReviewerIsTheOperator(t *testing.T) {
	got, _ := adviceFor(t, reviewerYAML, whoIs("pedoch"))

	if len(got) != 1 || !strings.Contains(got[0], "sinks.github-pr.reviewer") || !strings.Contains(got[0], "author") {
		t.Errorf("advice = %q, want one naming the setting and the author", got)
	}
}

func TestAdviseStaysQuietForAnotherReviewer(t *testing.T) {
	if got, _ := adviceFor(t, reviewerYAML, whoIs("someone-else")); len(got) != 0 {
		t.Errorf("advice = %q, want none", got)
	}
}

func TestAdviseAsksNothingWhenNoReviewerIsSet(t *testing.T) {
	got, f := adviceFor(t, "", whoIs("pedoch"))

	if len(got) != 0 {
		t.Errorf("advice = %q, want none", got)
	}
	for _, call := range f.Calls() {
		if strings.Contains(strings.Join(call.Argv(), " "), "api user") {
			t.Errorf("ran %v, want gh not asked who it is", call.Argv())
		}
	}
}

// An App installation token can't read /user, so not knowing is no reason to guess.
func TestAdviseStaysQuietWhenGhCannotSayWhoItIs(t *testing.T) {
	got, _ := adviceFor(t, reviewerYAML, func(root string) *proctest.Fake {
		f := ready(root)
		f.On(proctest.Response{Stderr: "Resource not accessible", ExitCode: 1}, "gh", "api", "user")
		return f
	})

	if len(got) != 0 {
		t.Errorf("advice = %q, want none", got)
	}
}
