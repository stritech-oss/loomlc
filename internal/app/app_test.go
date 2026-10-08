package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/config"
	"github.com/stritech-oss/loomlc/internal/proc/proctest"
)

// repo is a directory standing in for the operator's checkout, with the files loomlc reads from it.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "commit-policy.sh"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return dir
}

func files(dir string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, name))
	}
}

// ready config: the preset plus a repository to read tasks from and a check to run.
const readyYAML = `
sources:
  github:
    repo: acme/widgets
lifecycles:
  sdlc:
    steps:
      - name: qa
        gate: ["task check"]
`

// cfg parses one complete overlay over the preset.
func cfg(t *testing.T, yaml string) *config.Config {
	t.Helper()
	if yaml == "" {
		yaml = readyYAML
	}
	c, err := config.Parse("loomlc.yml", []byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

// without returns a fake where every tool but the named one is installed.
func without(missing string) *proctest.Fake {
	var fake proctest.Fake
	for _, cmd := range []string{"git", "gh", "claude"} {
		if cmd != missing {
			fake.SetPath(cmd, "/usr/bin/"+cmd)
		}
	}
	fake.On(proctest.Response{Stdout: ".git\n"}, "git", "rev-parse")
	fake.On(proctest.Response{Stdout: "git@github.com:acme/widgets.git\n"}, "git", "remote")
	return &fake
}

func newApp(t *testing.T, c *config.Config, dir string, fake *proctest.Fake) *App {
	t.Helper()
	a, err := New(Options{
		Config:   c,
		Repo:     dir,
		Env:      []string{"HOME=/home/user", "GH_TOKEN=ghp-a-token-value"},
		Runner:   fake,
		ReadFile: files(dir),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

// ready returns a fake where every tool is installed and git answers about the repository.
func ready() *proctest.Fake {
	var fake proctest.Fake
	for _, cmd := range []string{"git", "gh", "claude"} {
		fake.SetPath(cmd, "/usr/bin/"+cmd)
	}
	fake.On(proctest.Response{Stdout: ".git\n"}, "git", "rev-parse")
	fake.On(proctest.Response{Stdout: "git@github.com:acme/widgets.git\n"}, "git", "remote")
	return &fake
}

func TestLifecycleBuildsWhatARunNeeds(t *testing.T) {
	dir := repo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTemplate(t, dir)

	lc, err := newApp(t, cfg(t, ""), dir, ready()).Lifecycle("")
	if err != nil {
		t.Fatalf("Lifecycle: %v", err)
	}
	if lc.Name != "sdlc" {
		t.Errorf("name = %q", lc.Name)
	}
	if len(lc.Steps) != 3 {
		t.Fatalf("steps = %d, want three", len(lc.Steps))
	}
	for _, s := range lc.Steps {
		if s.Agent == nil || s.Role == "" || s.Output == "" {
			t.Errorf("step %s = %+v, want an agent, a role and an output", s.Name, s)
		}
	}
	if !strings.Contains(lc.Steps[0].Role, "planning agent") {
		t.Errorf("the plan step's role isn't the built-in one: %q", lc.Steps[0].Role[:40])
	}
	if lc.Steps[2].Gate == nil {
		t.Error("the review step's gate wasn't wired")
	}
	if !lc.Verified {
		t.Error("a lifecycle with a gate reports itself unverified")
	}
	if !strings.Contains(lc.Templates.Template, "Done / Description") {
		t.Error("the pull request template wasn't read from the checkout")
	}
	if lc.Forge == nil || lc.Executor == nil || lc.Checks == nil || lc.Policy == nil {
		t.Error("something a run needs wasn't built")
	}
}

func TestLifecycleRefusesWhatItCannotBuild(t *testing.T) {
	dir := repo(t)
	withTemplate := repo(t)
	writeTemplate(t, withTemplate)

	tests := []struct {
		name string
		dir  string
		yaml string
		pick string
		want string
	}{
		{
			name: "an adapter that isn't written",
			dir:  withTemplate,
			yaml: "sources:\n  github:\n    repo: acme/widgets\nlifecycles:\n  sdlc:\n    steps:\n      - name: qa\n        provider: codex\n        model: gpt-5-codex\n        gate: [\"task check\"]\n",
			want: "codex adapter isn't written yet",
		},
		{
			name: "a lifecycle by the wrong name",
			dir:  withTemplate,
			pick: "nope",
			want: `no lifecycle named "nope"`,
		},
		{
			name: "no repository to read tasks from",
			dir:  withTemplate,
			yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: qa\n        gate: [\"task check\"]\n",
			want: "set it to owner/name",
		},
		{
			name: "no pull request template in the checkout",
			dir:  dir,
			want: "read the pull request template",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newApp(t, cfg(t, tt.yaml), tt.dir, ready()).Lifecycle(tt.pick)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Lifecycle = %v, want an error about %q", err, tt.want)
			}
		})
	}
}

// loomlc won't run without its own policy checker: it is the check that says what a run may commit.
func TestLifecycleNeedsThePolicyScript(t *testing.T) {
	dir := t.TempDir()
	_, err := newApp(t, cfg(t, ""), dir, ready()).Lifecycle("")
	if err == nil || !strings.Contains(err.Error(), "commit-policy.sh") {
		t.Errorf("Lifecycle = %v, want it to name the missing script", err)
	}
}

func TestNewRefusesOptionsItCannotUse(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{name: "no configuration", opts: Options{Repo: "/repo", Runner: &proctest.Fake{}}, want: "no configuration"},
		{name: "a relative repository", opts: Options{Config: &config.Config{}, Repo: "repo", Runner: &proctest.Fake{}}, want: "must be absolute"},
		{name: "no runner", opts: Options{Config: &config.Config{}, Repo: "/repo"}, want: "no process runner"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.opts); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("New = %v, want an error about %q", err, tt.want)
			}
		})
	}
}

func TestSecretsMaskTheEnvironmentItWasGiven(t *testing.T) {
	a := newApp(t, cfg(t, ""), repo(t), ready())
	if got := a.Secrets().String("the token is ghp-a-token-value"); strings.Contains(got, "ghp-a-token-value") {
		t.Errorf("String = %q, want the value from the environment masked", got)
	}
}

func writeTemplate(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "## Done / Description\n\n<!-- what -->\n\n## QA\n\n<!-- how -->\n\n## Issue / Card\n\n<!-- which -->\n"
	if err := os.WriteFile(filepath.Join(dir, ".github", "pull_request_template.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
}
