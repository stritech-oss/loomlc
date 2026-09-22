package config

import (
	"strings"
	"testing"
)

func TestParseReportsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr []string
	}{
		{name: "unsupported version", yaml: "version: 2\n", wantErr: []string{"version: must be 1, got 2"}},
		{name: "uppercase provider name", yaml: "providers:\n  Claude:\n    adapter: claude\n", wantErr: []string{`providers.Claude: name "Claude" must start with a lowercase letter`}},
		{name: "unknown adapter", yaml: "providers:\n  gemini: {}\n", wantErr: []string{`providers.gemini.adapter: unknown adapter "gemini"`}},
		{name: "command with arguments", yaml: "providers:\n  claude:\n    cmd: claude --verbose\n", wantErr: []string{"providers.claude.cmd:", "single executable"}},
		{name: "permission mode on codex", yaml: "providers:\n  codex:\n    permission_mode: auto\n", wantErr: []string{"providers.codex.permission_mode: only applies to the claude adapter"}},
		{name: "unknown permission mode", yaml: "providers:\n  claude:\n    permission_mode: yolo\n", wantErr: []string{`unknown mode "yolo"`}},
		{name: "docker executor", yaml: "executors:\n  local:\n    type: docker\n", wantErr: []string{"executors.local.type: the docker executor isn't available until Phase 5"}},
		{name: "workspace root outside the repository", yaml: "executors:\n  local:\n    root: ../worktrees\n", wantErr: []string{"executors.local.root:", "must stay inside the repository"}},
		{name: "unknown source type", yaml: "sources:\n  github:\n    type: gitlab\n", wantErr: []string{`sources.github.type: unknown source type "gitlab"`}},
		{name: "malformed repository", yaml: "sources:\n  github:\n    repo: not-a-repo\n", wantErr: []string{"sources.github.repo:", "owner/name"}},
		{name: "one label for two purposes", yaml: "sinks:\n  github-pr:\n    label: agent-revise\n", wantErr: []string{`sinks.github-pr.label: label "agent-revise" is already used by feedback.label`}},
		{name: "invalid reviewer", yaml: "sinks:\n  github-pr:\n    reviewer: -nope\n", wantErr: []string{"sinks.github-pr.reviewer:", "isn't a valid GitHub username"}},
		{name: "absolute template path", yaml: "sinks:\n  github-pr:\n    template: /etc/template.md\n", wantErr: []string{"sinks.github-pr.template:", "must be relative to the repository"}},
		{name: "undefined executor", yaml: "lifecycles:\n  sdlc:\n    executor: sandbox\n", wantErr: []string{`lifecycles.sdlc.executor: names executor "sandbox"`}},
		{name: "zero concurrency", yaml: "lifecycles:\n  sdlc:\n    concurrency: 0\n", wantErr: []string{"lifecycles.sdlc.concurrency: must be at least 1"}},
		{name: "short watch interval", yaml: "lifecycles:\n  sdlc:\n    watch_interval: 10s\n", wantErr: []string{"lifecycles.sdlc.watch_interval: must be at least 30s"}},
		{name: "branch shared by every task", yaml: "lifecycles:\n  sdlc:\n    branch: agent/work\n", wantErr: []string{"lifecycles.sdlc.branch: must use {{.ID}}"}},
		{name: "branch keyed on the title alone", yaml: "lifecycles:\n  sdlc:\n    branch: \"feat/{{.Slug}}\"\n", wantErr: []string{"lifecycles.sdlc.branch: must use {{.ID}}", "same title"}},
		{name: "branch component starting with a dot", yaml: "lifecycles:\n  sdlc:\n    branch: \".feat/issue-{{.ID}}\"\n", wantErr: []string{"isn't a valid git branch name"}},
		{name: "branch component ending in .lock", yaml: "lifecycles:\n  sdlc:\n    branch: \"feat.lock/issue-{{.ID}}\"\n", wantErr: []string{"isn't a valid git branch name"}},
		{name: "workspace root is the repository", yaml: "executors:\n  local:\n    root: .\n", wantErr: []string{"executors.local.root: must be a directory of its own"}},
		{name: "workspace root inside .git", yaml: "executors:\n  local:\n    root: .git/loomlc\n", wantErr: []string{"executors.local.root: must not be inside .git"}},
		{name: "branch with a space", yaml: "lifecycles:\n  sdlc:\n    branch: \"feat/{{.ID}} x\"\n", wantErr: []string{"isn't a valid git branch name"}},
		{name: "branch template with an unknown field", yaml: "lifecycles:\n  sdlc:\n    branch: \"feat/{{.Title}}\"\n", wantErr: []string{"use {{.ID}} and {{.Slug}}"}},
		{name: "pi without vendor or model", yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: qa\n        provider: pi\n", wantErr: []string{"steps[2].vendor: is required for pi", "steps[2].model: is required for pi"}},
		{name: "vendor on a claude step", yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: plan\n        vendor: anthropic\n", wantErr: []string{"steps[0].vendor: only applies to aggregator providers"}},
		{name: "verdict step that can edit", yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: qa\n        readonly: false\n", wantErr: []string{"steps[2].readonly: must be true"}},
		{name: "gate on the change step", yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: engineer\n        gate: [go test ./...]\n", wantErr: []string{"steps[1].gate: gates run just before a verdict step"}},
		{name: "too many iterations", yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: engineer\n        max_iter: 50\n", wantErr: []string{"steps[1].max_iter: must be between 1 and 20, got 50"}},
		{name: "timeout over four hours", yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: qa\n        timeout: 5h\n", wantErr: []string{"steps[2].timeout: must be more than 0 and at most 4h"}},
		{name: "unknown role", yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: plan\n        role: architect\n", wantErr: []string{`steps[0].role: unknown role "architect"`}},
		{name: "custom role without an output", yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: plan\n        role: prompts/planner.md\n", wantErr: []string{"steps[0].output: is required for a custom role"}},
		{name: "role and output disagree", yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: plan\n        output: verdict\n", wantErr: []string{"steps[0].output: the planner role produces a plan, not a verdict"}},
		{name: "fourth step", yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: review\n        provider: claude\n        role: qa\n        readonly: true\n        timeout: 10m\n", wantErr: []string{"lifecycles.sdlc.steps: Phase 0 lifecycles need exactly three steps", "found 4 steps"}},
		{name: "feedback without the loop", yaml: "lifecycles:\n  sdlc:\n    feedback:\n      steps: [qa]\n", wantErr: []string{"lifecycles.sdlc.feedback.steps: must be [engineer, qa]"}},
		{name: "every problem at once", yaml: "version: 2\nlifecycles:\n  sdlc:\n    concurrency: 0\n", wantErr: []string{"version: must be 1", "concurrency: must be at least 1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("loomlc.yml", []byte(tt.yaml))
			if err == nil {
				t.Fatal("Parse succeeded, want a validation error")
			}
			if !strings.Contains(err.Error(), "invalid configuration in loomlc.yml") {
				t.Errorf("error = %v, want it to name the file", err)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %v\nwant it to contain %q", err, want)
				}
			}
		})
	}
}

func TestBranchProblem(t *testing.T) {
	valid := []string{"main", "feat/issue-12-add-config", "release/v1.2"}
	invalid := []string{"", "feat//x", "feat/a..b", "feat/x.lock", "-flag", "feat/", "has space", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b", "a@{b"}
	for _, name := range valid {
		if msg := branchProblem(name); msg != "" {
			t.Errorf("branchProblem(%q) = %q, want valid", name, msg)
		}
	}
	for _, name := range invalid {
		if branchProblem(name) == "" {
			t.Errorf("branchProblem(%q) = \"\", want a problem", name)
		}
	}
}
