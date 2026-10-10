package config

import "testing"

func TestBranchNameReadsAsTheTask(t *testing.T) {
	const tmpl = "feat/issue-{{.ID}}-{{.Slug}}"
	tests := []struct {
		name, id, title, want string
	}{
		{name: "a plain title", id: "9", title: "Print the build commit", want: "feat/issue-9-print-the-build-commit"},
		{name: "punctuation and case", id: "9", title: "feat(cli): print `loomlc version`!", want: "feat/issue-9-feat-cli-print-loomlc-version"},
		{name: "a path in the title", id: "9", title: "../../etc/passwd", want: "feat/issue-9-etc-passwd"},
		{name: "a long title is cut at a word", id: "9", title: "Decide whether a task's state is a boolean or an enum, in PR 10", want: "feat/issue-9-decide-whether-a-task-s-state-is-a-boolean-or-an"},
		{name: "one word longer than the limit is cut inside it", id: "9", title: "Supercalifragilisticexpialidociousandthensomemorelettersherefortesting", want: "feat/issue-9-supercalifragilisticexpialidociousandthensomemorel"},
		{name: "a long word after a short one takes the short one", id: "9", title: "x Supercalifragilisticexpialidociousandthensomemoreletters", want: "feat/issue-9-x"},
		{name: "nothing a branch can use", id: "9", title: "日本語", want: "feat/issue-9-task"},
		{name: "an empty title", id: "9", title: "", want: "feat/issue-9-task"},
		{name: "an id that isn't a number", id: "PROJ-123", title: "Add a thing", want: "feat/issue-PROJ-123-add-a-thing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BranchName(tmpl, tt.id, tt.title)
			if err != nil {
				t.Fatalf("BranchName: %v", err)
			}
			if got != tt.want {
				t.Errorf("branch = %q, want %q", got, tt.want)
			}
		})
	}
}

// A slug can't make a bad branch name, but a template with a task's id in the wrong place can, and the
// id comes from outside loomlc.
func TestBranchNameRefusesANameGitWouldnt(t *testing.T) {
	if _, err := BranchName("feat/{{.ID}}/{{.Slug}}", ".hidden", "Add a thing"); err == nil {
		t.Error("BranchName accepted a branch component starting with a dot")
	}
	if _, err := BranchName("feat/{{.Nope}}", "9", "Add a thing"); err == nil {
		t.Error("BranchName accepted a template field that doesn't exist")
	}
}
