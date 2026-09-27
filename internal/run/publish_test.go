package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/gate"
	"github.com/stritech-oss/loomlc/internal/git"
)

type vcs struct {
	commits []git.Commit
	paths   []string
	diff    string
	foldErr error
	folded  bool
}

func (v *vcs) Autosquash(context.Context, string, string) error {
	v.folded = true
	return v.foldErr
}
func (v *vcs) Log(context.Context, string, string) ([]git.Commit, error) { return v.commits, nil }
func (v *vcs) PathsSince(context.Context, string, string) ([]string, error) {
	return v.paths, nil
}
func (v *vcs) Diff(context.Context, string, string) (string, error) { return v.diff, nil }

type checks struct {
	results []gate.Result
	ran     bool
}

func (c *checks) Run(_ context.Context, _ string, commands [][]string) ([]gate.Result, error) {
	if len(commands) == 0 {
		return nil, nil
	}
	c.ran = true
	return c.results, nil
}

type policy struct{ err error }

func (p policy) CheckRange(context.Context, string, string, string) error { return p.err }

type scanner struct{ matches []Match }

func (s scanner) Scan(string) []Match { return s.matches }

func options() Options {
	return Options{
		Dir:            "/work/issue-9",
		Base:           "abc1234",
		Branch:         "feat/issue-9",
		ProtectedPaths: []string{"loomlc.yml", "prompts"},
		Gates:          [][]string{{"task", "check"}},
		Verified:       true,
	}
}

func work() *vcs {
	return &vcs{
		commits: []git.Commit{{SHA: "1111111", Subject: "feat(cli): print the build commit"}},
		paths:   []string{"internal/cli/version.go"},
	}
}

func TestWorkThatIsReadyIsPublishable(t *testing.T) {
	v, c := work(), &checks{results: []gate.Result{{Name: "task check", Passed: true}}}

	d, err := New(v, c, policy{}, nil).Prepare(context.Background(), options())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !d.OK() {
		t.Fatalf("refusals = %q, want none", d.Refusals)
	}
	if !v.folded {
		t.Error("the run's own fixups weren't folded before the checks")
	}
	if !c.ran {
		t.Error("the publish gates didn't run")
	}
	if len(d.Commits) != 1 {
		t.Errorf("commits = %+v, want the branch's commits", d.Commits)
	}
}

func TestPrepareRefuses(t *testing.T) {
	tests := []struct {
		name    string
		vcs     *vcs
		checks  *checks
		policy  policy
		scanner Scanner
		edit    func(*Options)
		want    string
	}{
		{
			name: "nothing built or tested the work",
			vcs:  work(), checks: &checks{},
			edit: func(o *Options) { o.Verified = false },
			want: "nothing in this lifecycle builds or tests",
		},
		{
			name: "the fixups wouldn't fold",
			vcs:  &vcs{foldErr: errors.New("the tree changed from abc to def")}, checks: &checks{},
			want: "couldn't be folded in",
		},
		{
			name: "nothing to publish",
			vcs:  &vcs{}, checks: &checks{},
			want: "nothing to publish",
		},
		{
			name: "a protected file was changed",
			vcs: &vcs{
				commits: []git.Commit{{SHA: "1111111", Subject: "feat: a"}},
				paths:   []string{"internal/a.go", "prompts/engineer.md"},
			},
			checks: &checks{},
			want:   "prompts/engineer.md is protected",
		},
		{
			name: "a credential in the diff",
			vcs:  work(), checks: &checks{},
			scanner: scanner{matches: []Match{{Path: "a.go", Line: 12, Rule: "something shaped like a credential"}}},
			want:    "a.go:12 adds something shaped like a credential",
		},
		{
			name: "the commits don't pass the policy",
			vcs:  work(), checks: &checks{},
			policy: policy{err: errors.New(`header is not a Conventional Commit: "Add stuff"`)},
			want:   "don't pass the commit policy",
		},
		{
			name:   "a publish gate failed",
			vcs:    work(),
			checks: &checks{results: []gate.Result{{Name: "task check", Passed: false, Output: "lint: 3 issues"}}},
			want:   "the publish gate `task check` failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := options()
			if tt.edit != nil {
				tt.edit(&opts)
			}
			d, err := New(tt.vcs, tt.checks, tt.policy, tt.scanner).Prepare(context.Background(), opts)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if d.OK() {
				t.Fatalf("Prepare allowed it, want a refusal about %q", tt.want)
			}
			if !strings.Contains(strings.Join(d.Refusals, "\n"), tt.want) {
				t.Errorf("refusals = %q, want one containing %q", d.Refusals, tt.want)
			}
		})
	}
}

// An unverified lifecycle that says so publishes, and the pull request has to say it too.
func TestUnverifiedWorkPublishesWithAWarning(t *testing.T) {
	opts := options()
	opts.Verified, opts.AllowUnverified = false, true

	d, err := New(work(), &checks{results: []gate.Result{{Name: "task check", Passed: true}}}, policy{}, nil).Prepare(context.Background(), opts)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !d.OK() {
		t.Fatalf("refusals = %q, want none", d.Refusals)
	}
	if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "nothing built or tested") {
		t.Errorf("warnings = %q, want the reader told", d.Warnings)
	}
}

// A run that can't be published should say everything that's wrong, not just the first thing.
func TestPrepareReportsEveryProblemItCanSeeAtOnce(t *testing.T) {
	v := &vcs{
		commits: []git.Commit{{SHA: "1111111", Subject: "Add stuff"}},
		paths:   []string{"loomlc.yml", "prompts/qa.md"},
	}
	c := &checks{results: []gate.Result{{Name: "task check", Passed: false, Output: "lint: 3 issues"}}}
	s := scanner{matches: []Match{{Path: "a.go", Line: 3, Rule: "a secret from this run's own environment"}}}

	d, err := New(v, c, policy{err: errors.New("not a Conventional Commit")}, s).Prepare(context.Background(), options())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(d.Refusals) != 5 {
		t.Errorf("refusals = %d, want five: two protected paths, a credential, the policy and the gate:\n%s", len(d.Refusals), strings.Join(d.Refusals, "\n"))
	}
}

// A protected directory covers what's under it, and a path that merely starts with the same letters isn't
// under it.
func TestProtectedPathsCoverDirectories(t *testing.T) {
	hits := protectedChanges(
		[]string{"prompts/engineer.md", "promptsmith/a.go", "loomlc.yml", "docs/loomlc.yml.md"},
		[]string{"prompts", "loomlc.yml"},
	)
	if fmt.Sprint(hits) != "[prompts/engineer.md loomlc.yml]" {
		t.Errorf("hits = %q", hits)
	}
}
