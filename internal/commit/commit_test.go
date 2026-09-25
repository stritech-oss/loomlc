package commit

import (
	"strings"
	"testing"
)

// branch is the commit list a prompt shows the change step.
func branch() []Existing {
	return []Existing{
		{SHA: "3f1c2ab9d4e5f60718293a4b5c6d7e8f90123456", Subject: "feat(version): add a build-stamped version"},
		{SHA: "9b7d004a1b2c3d4e5f60718293a4b5c6d7e8f901", Subject: "test(version): cover the unstamped default"},
		{SHA: "aa11bb22cc33dd44ee55ff6677889900aabbccdd", Subject: "fixup! feat(version): add a build-stamped version"},
	}
}

// details renders findings the way a prompt does — path first — so a test reads what the agent reads.
func details(findings []Finding) string {
	var parts []string
	for _, f := range findings {
		if f.Path != "" {
			parts = append(parts, f.Path+" "+f.Detail)
			continue
		}
		parts = append(parts, f.Detail)
	}
	return strings.Join(parts, "\n")
}

func TestCheckAcceptsWorkThatMatchesTheWorkspace(t *testing.T) {
	proposed := []Proposed{
		{Message: "feat(cli): print the build commit\n\nReads the stamped version.\n", Paths: []string{"internal/cli/version.go"}},
		{Message: "test(cli): cover the unstamped default", Paths: []string{"internal/cli/version_test.go"}},
		{Message: "the version was mutable at package level", Paths: []string{"internal/version/version.go"}, Fixes: "3f1c2ab"},
	}
	changed := []string{"internal/cli/version.go", "internal/cli/version_test.go", "internal/version/version.go"}

	if got := Check(proposed, changed, branch()); got != nil {
		t.Errorf("findings = %v, want none", details(got))
	}
}

func TestCheckReportsWhatTheEngineCannotDo(t *testing.T) {
	changed := []string{"a.go", "b.go"}
	tests := []struct {
		name     string
		proposed []Proposed
		changed  []string
		want     string
	}{
		{
			name:    "a changed file nobody claimed",
			changed: changed,
			proposed: []Proposed{
				{Message: "feat(a): add a", Paths: []string{"a.go"}},
			},
			want: "b.go",
		},
		{
			name:    "one file in two commits",
			changed: changed,
			proposed: []Proposed{
				{Message: "feat(a): add a", Paths: []string{"a.go", "b.go"}},
				{Message: "refactor(a): tidy a", Paths: []string{"a.go"}},
			},
			want: "already in commit 1",
		},
		{
			name:    "a path with nothing to commit",
			changed: changed,
			proposed: []Proposed{
				{Message: "feat(a): add a", Paths: []string{"a.go", "b.go", "untouched.go"}},
			},
			want: "nothing to commit",
		},
		{
			name:     "no commits at all",
			changed:  changed,
			proposed: nil,
			want:     "no commits were proposed",
		},
		{
			name:     "a commit with no paths",
			changed:  nil,
			proposed: []Proposed{{Message: "feat(a): add a"}},
			want:     "has no paths",
		},
		{
			name:     "a message that isn't a Conventional Commit",
			changed:  []string{"a.go"},
			proposed: []Proposed{{Message: "Add the thing", Paths: []string{"a.go"}}},
			want:     "isn't a Conventional Commit",
		},
		{
			name:     "a subject over the limit",
			changed:  []string{"a.go"},
			proposed: []Proposed{{Message: "feat(cli): " + strings.Repeat("x", 70), Paths: []string{"a.go"}}},
			want:     "the limit is 72",
		},
		{
			name:     "a subject ending in a period",
			changed:  []string{"a.go"},
			proposed: []Proposed{{Message: "feat(cli): print the build commit.", Paths: []string{"a.go"}}},
			want:     "ends with a period",
		},
		{
			name:     "no blank line before the body",
			changed:  []string{"a.go"},
			proposed: []Proposed{{Message: "feat(cli): print the commit\nand explain it", Paths: []string{"a.go"}}},
			want:     "blank line",
		},
		{
			name:     "a fix naming a commit that isn't on the branch",
			changed:  []string{"a.go"},
			proposed: []Proposed{{Message: "it was wrong", Paths: []string{"a.go"}, Fixes: "deadbeef"}},
			want:     "isn't a commit on this branch",
		},
		{
			name:     "a fix named too loosely to resolve",
			changed:  []string{"a.go"},
			proposed: []Proposed{{Message: "it was wrong", Paths: []string{"a.go"}, Fixes: "3f1"}},
			want:     "too short",
		},
		{
			name:     "a fix of a fix",
			changed:  []string{"a.go"},
			proposed: []Proposed{{Message: "it was still wrong", Paths: []string{"a.go"}, Fixes: "aa11bb2"}},
			want:     "itself a fix",
		},
		{
			name:     "a fix with no explanation",
			changed:  []string{"a.go"},
			proposed: []Proposed{{Paths: []string{"a.go"}, Fixes: "3f1c2ab"}},
			want:     "without saying what was wrong",
		},
		{
			name:     "a subject the branch already uses",
			changed:  []string{"a.go"},
			proposed: []Proposed{{Message: "test(version): cover the unstamped default", Paths: []string{"a.go"}}},
			want:     "already the subject of another commit",
		},
		{
			name:    "two proposed commits sharing a subject",
			changed: changed,
			proposed: []Proposed{
				{Message: "feat(a): add a", Paths: []string{"a.go"}},
				{Message: "feat(a): add a", Paths: []string{"b.go"}},
			},
			want: "already the subject of another commit",
		},
		{
			name:     "a path outside the repository",
			changed:  []string{"a.go"},
			proposed: []Proposed{{Message: "feat(a): add a", Paths: []string{"../../etc/passwd"}}},
			want:     "outside the repository",
		},
		{
			name:     "an absolute path",
			changed:  []string{"a.go"},
			proposed: []Proposed{{Message: "feat(a): add a", Paths: []string{"/etc/passwd"}}},
			want:     "absolute path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := details(Check(tt.proposed, tt.changed, branch()))
			if !strings.Contains(got, tt.want) {
				t.Errorf("findings =\n%s\nwant one containing %q", got, tt.want)
			}
		})
	}
}

// A finding says which commit and which file it's about, because the agent has to act on it.
func TestFindingsSayWhereTheProblemIs(t *testing.T) {
	proposed := []Proposed{
		{Message: "feat(a): add a", Paths: []string{"a.go"}},
		{Message: "feat(b): add b", Paths: []string{"a.go"}},
	}
	findings := Check(proposed, []string{"a.go", "orphan.go"}, nil)

	var duplicate, uncovered bool
	for _, f := range findings {
		if f.Commit == 2 && f.Path == "a.go" {
			duplicate = true
		}
		if f.Commit == 0 && f.Path == "orphan.go" {
			uncovered = true
		}
	}
	if !duplicate {
		t.Errorf("findings = %+v, want the duplicate path blamed on commit 2", findings)
	}
	if !uncovered {
		t.Errorf("findings = %+v, want the uncovered file reported against no commit", findings)
	}
}

// A path is compared as git sees it, so "./a.go" and "a.go" are the same file.
func TestCheckNormalizesPaths(t *testing.T) {
	proposed := []Proposed{{Message: "feat(a): add a", Paths: []string{"./internal/a.go"}}}
	if got := Check(proposed, []string{"internal/a.go"}, nil); got != nil {
		t.Errorf("findings = %v, want none", details(got))
	}
}
