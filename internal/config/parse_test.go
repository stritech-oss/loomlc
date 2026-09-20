package config

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func mustParse(t *testing.T, data string) *Config {
	t.Helper()
	c, err := Parse("loomlc.yml", []byte(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

// check compares a resolved value by its printed form, which keeps table entries short.
type check struct {
	name      string
	got, want any
}

func runChecks(t *testing.T, checks []check) {
	t.Helper()
	for _, c := range checks {
		if got, want := fmt.Sprint(c.got), fmt.Sprint(c.want); got != want {
			t.Errorf("%s = %s, want %s", c.name, got, want)
		}
	}
}

func stepNames(lc Lifecycle) []string {
	names := make([]string, len(lc.Steps))
	for i, s := range lc.Steps {
		names[i] = s.Name
	}
	return names
}

func TestParseResolvesThePreset(t *testing.T) {
	c := mustParse(t, "")
	sdlc := c.Lifecycles["sdlc"]

	runChecks(t, []check{
		{"lifecycles", len(c.Lifecycles), 1},
		{"executor", sdlc.Executor, "local"},
		{"source", sdlc.Source, "github"},
		{"sink", sdlc.Sink, "github-pr"},
		{"concurrency", sdlc.Concurrency, 3},
		{"max open outputs", sdlc.MaxOpenOutputs, 5},
		{"watch interval", time.Duration(sdlc.WatchInterval), 5 * time.Minute},
		{"branch", sdlc.Branch, "feat/issue-{{.ID}}-{{.Slug}}"},
		{"steps", stepNames(sdlc), []string{"plan", "engineer", "qa"}},
		{"plan output", sdlc.Steps[0].Output, OutputPlan},
		{"engineer output", sdlc.Steps[1].Output, OutputChange},
		{"qa output", sdlc.Steps[2].Output, OutputVerdict},
		{"engineer loop", sdlc.Steps[1].LoopWith, "qa"},
		{"engineer max iterations", sdlc.Steps[1].MaxIter, 5},
		{"feedback steps", sdlc.Feedback.Steps, []string{"engineer", "qa"}},
		{"claude adapter", c.Providers["claude"].Adapter, AdapterClaude},
		{"pi command", c.Providers["pi"].Cmd, "pi"},
		{"trigger label", c.Sources["github"].Trigger.Label, "agent-ready"},
		{"pull request label", c.Sinks["github-pr"].Label, "agent-pr"},
		{"feedback label", c.Sinks["github-pr"].Feedback.Label, "agent-revise"},
	})
}

func TestParseTreatsCommentOnlyFileAsThePreset(t *testing.T) {
	c := mustParse(t, "# nothing to change yet\n")
	if got := c.Lifecycles["sdlc"].Concurrency; got != 3 {
		t.Errorf("concurrency = %d, want the preset's 3", got)
	}
}

func TestParseMergesOverThePreset(t *testing.T) {
	tests := []struct {
		name   string
		yaml   string
		checks func(c *Config) []check
	}{
		{
			name: "scalars override and the rest is kept",
			yaml: "lifecycles:\n  sdlc:\n    concurrency: 1\n",
			checks: func(c *Config) []check {
				return []check{
					{"concurrency", c.Lifecycles["sdlc"].Concurrency, 1},
					{"max open outputs", c.Lifecycles["sdlc"].MaxOpenOutputs, 5},
				}
			},
		},
		{
			name: "lists replace",
			yaml: "lifecycles:\n  sdlc:\n    protected_paths: [docs]\n",
			checks: func(c *Config) []check {
				return []check{{"protected paths", c.Lifecycles["sdlc"].ProtectedPaths, []string{"docs"}}}
			},
		},
		{
			name: "steps merge by name",
			yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: qa\n        timeout: 10m\n",
			checks: func(c *Config) []check {
				sdlc := c.Lifecycles["sdlc"]
				return []check{
					{"steps", stepNames(sdlc), []string{"plan", "engineer", "qa"}},
					{"qa timeout", time.Duration(sdlc.Steps[2].Timeout), 10 * time.Minute},
					{"qa model", sdlc.Steps[2].Model, "sonnet"},
					{"engineer timeout", time.Duration(sdlc.Steps[1].Timeout), time.Hour},
				}
			},
		},
		{
			name: "switching a step's provider drops the old model",
			yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: qa\n        provider: codex\n",
			checks: func(c *Config) []check {
				qa := c.Lifecycles["sdlc"].Steps[2]
				return []check{{"qa provider", qa.Provider, "codex"}, {"qa model", qa.Model, ""}}
			},
		},
		{
			name: "one provider per role",
			yaml: `lifecycles:
  sdlc:
    steps:
      - name: plan
        provider: pi
        vendor: google
        model: gemini-3-pro
      - name: qa
        provider: codex
        model: gpt-5-codex
`,
			checks: func(c *Config) []check {
				steps := c.Lifecycles["sdlc"].Steps
				return []check{
					{"plan", []string{steps[0].Provider, steps[0].Vendor, steps[0].Model}, []string{"pi", "google", "gemini-3-pro"}},
					{"engineer provider", steps[1].Provider, "claude"},
					{"qa", []string{steps[2].Provider, steps[2].Model}, []string{"codex", "gpt-5-codex"}},
				}
			},
		},
		{
			name: "a provider's adapter and command default to its name and adapter",
			yaml: "providers:\n  fast-claude:\n    adapter: claude\n",
			checks: func(c *Config) []check {
				p := c.Providers["fast-claude"]
				return []check{{"adapter", p.Adapter, AdapterClaude}, {"cmd", p.Cmd, "claude"}}
			},
		},
		{
			name: "gates accept strings and lists",
			yaml: "lifecycles:\n  sdlc:\n    steps:\n      - name: qa\n        gate: [\"go vet ./...\", [go, test, -race, ./...]]\n",
			checks: func(c *Config) []check {
				return []check{{"gates", c.Lifecycles["sdlc"].Steps[2].Gate, []Command{{"go", "vet", "./..."}, {"go", "test", "-race", "./..."}}}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := mustParse(t, tt.yaml)
			runChecks(t, tt.checks(c))
		})
	}
}

func TestMergeAppendsStepsWithNewNames(t *testing.T) {
	base := mustNode(t, "lifecycles:\n  sdlc:\n    steps:\n      - name: plan\n      - name: qa\n")
	over := mustNode(t, "lifecycles:\n  sdlc:\n    steps:\n      - name: review\n")

	var c Config
	if err := mergeNode(base, over, nil).Decode(&c); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got, want := stepNames(c.Lifecycles["sdlc"]), []string{"plan", "qa", "review"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("steps = %v, want %v", got, want)
	}
	if got := stepNames(mustDecode(t, base).Lifecycles["sdlc"]); len(got) != 2 {
		t.Errorf("merging modified the base: steps = %v", got)
	}
}

func TestParseRejectsMalformedFiles(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr []string
	}{
		{name: "unknown key", yaml: "lifecycles:\n  sdlc:\n    concurency: 2\n", wantErr: []string{"parse loomlc.yml", "line 3", "concurency"}},
		{name: "top level is a list", yaml: "- sdlc\n", wantErr: []string{"parse loomlc.yml"}},
		{name: "bad duration", yaml: "lifecycles:\n  sdlc:\n    watch_interval: soon\n", wantErr: []string{`invalid duration "soon"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("loomlc.yml", []byte(tt.yaml))
			for _, want := range tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("error = %v, want it to contain %q", err, want)
				}
			}
		})
	}
}

func mustNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return doc.Content[0]
}

func mustDecode(t *testing.T, n *yaml.Node) Config {
	t.Helper()
	var c Config
	if err := n.Decode(&c); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return c
}
