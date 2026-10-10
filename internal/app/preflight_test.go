package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/proc/proctest"
)

func TestPreflightPassesWhenEverythingIsThere(t *testing.T) {
	dir := repo(t)
	writeTemplate(t, dir)
	a := newApp(t, cfg(t, ""), dir, ready(dir))
	lc, err := a.Lifecycle("")
	if err != nil {
		t.Fatalf("Lifecycle: %v", err)
	}

	if problems := a.Preflight(context.Background(), lc); len(problems) != 0 {
		t.Errorf("problems = %q, want none", problems)
	}
}

func TestPreflightReportsWhatStopsARun(t *testing.T) {
	dir := repo(t)
	writeTemplate(t, dir)

	unverified := "sources:\n  github:\n    repo: acme/widgets\n"

	tests := []struct {
		name string
		yaml string
		fake func(root string) *proctest.Fake
		want string
	}{
		{
			name: "a lifecycle that checks nothing",
			yaml: unverified,
			fake: ready,
			want: "builds or tests the work",
		},
		{
			name: "an agent CLI that isn't installed",
			fake: func(root string) *proctest.Fake { return without(root, "claude") },
			want: "claude isn't on PATH",
		},
		{
			name: "a directory that isn't a repository",
			fake: func(root string) *proctest.Fake {
				f := without(root, "")
				f.On(proctest.Response{Stderr: "not a git repository", ExitCode: 128}, "git", "rev-parse")
				return f
			},
			want: "isn't a git repository",
		},
		{
			name: "a remote that isn't configured",
			fake: func(root string) *proctest.Fake {
				f := ready(root)
				f.On(proctest.Response{Stderr: "No such remote", ExitCode: 2}, "git", "remote")
				return f
			},
			want: `remote "origin" isn't configured`,
		},
		{
			name: "loomlc started from a subdirectory",
			fake: func(root string) *proctest.Fake {
				// A run writes beside the checkout and reads its policy script from it, so the root is
				// the only place it can start.
				return ready(filepath.Dir(root))
			},
			want: "from the repository's root",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newApp(t, cfg(t, tt.yaml), dir, tt.fake(dir))
			lc, err := a.Lifecycle("")
			if err != nil {
				t.Fatalf("Lifecycle: %v", err)
			}
			problems := a.Preflight(context.Background(), lc)
			if !strings.Contains(strings.Join(problems, "\n"), tt.want) {
				t.Errorf("problems = %q, want one about %q", problems, tt.want)
			}
		})
	}
}

// An unverified lifecycle that says so is allowed to run.
func TestPreflightAllowsAnUnverifiedLifecycleThatSaysSo(t *testing.T) {
	dir := repo(t)
	writeTemplate(t, dir)
	yaml := "sources:\n  github:\n    repo: acme/widgets\nlifecycles:\n  sdlc:\n    publish:\n      allow_unverified: true\n"
	a := newApp(t, cfg(t, yaml), dir, ready(dir))
	lc, err := a.Lifecycle("")
	if err != nil {
		t.Fatalf("Lifecycle: %v", err)
	}

	if problems := a.Preflight(context.Background(), lc); len(problems) != 0 {
		t.Errorf("problems = %q, want none", problems)
	}
}
