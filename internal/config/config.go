// Package config loads loomlc.yml, merges it over the built-in sdlc preset, fills in defaults, and
// validates the result, so the rest of loomlc can trust what it reads.
package config

// Adapters a provider can use.
const (
	AdapterClaude = "claude"
	AdapterCodex  = "codex"
	AdapterPi     = "pi"
)

// Step outputs: what a step's final answer contains.
const (
	OutputPlan    = "plan"
	OutputChange  = "change"
	OutputVerdict = "verdict"
)

// Config is a loomlc configuration.
type Config struct {
	Version    int                  `yaml:"version"`
	Providers  map[string]Provider  `yaml:"providers"`
	Executors  map[string]Executor  `yaml:"executors"`
	Sources    map[string]Source    `yaml:"sources"`
	Sinks      map[string]Sink      `yaml:"sinks"`
	Lifecycles map[string]Lifecycle `yaml:"lifecycles"`
}

// Provider configures an agent CLI that runs steps.
type Provider struct {
	// Adapter is the implementation: claude, codex, or pi. It defaults to the provider's name.
	Adapter string `yaml:"adapter"`
	// Cmd is the executable, as a name or a path. It defaults to the adapter's name.
	Cmd string `yaml:"cmd"`
	// PermissionMode is the claude permission mode for steps that edit files.
	PermissionMode string `yaml:"permission_mode"`
}

// Executor configures where a run's workspace lives.
type Executor struct {
	// Type is the executor implementation. Phase 0 has only worktree.
	Type string `yaml:"type"`
	// Root is the directory, relative to the checkout, that holds workspaces.
	Root string `yaml:"root"`
	// Remote is the git remote workspaces fetch from and push to.
	Remote string `yaml:"remote"`
}

// Source configures where tasks come from.
type Source struct {
	// Type is the source implementation. Phase 0 has only github.
	Type string `yaml:"type"`
	// Repo is the GitHub repository as owner/name. Empty means the checkout's own repository.
	Repo    string       `yaml:"repo"`
	Trigger Trigger      `yaml:"trigger"`
	Labels  SourceLabels `yaml:"labels"`
}

// Trigger says which tasks are ready to pick up.
type Trigger struct {
	Label string `yaml:"label"`
}

// SourceLabels are the labels that track a task through a run.
type SourceLabels struct {
	InProgress string `yaml:"in_progress"`
	Done       string `yaml:"done"`
	Failed     string `yaml:"failed"`
}

// Sink configures where results go.
type Sink struct {
	// Type is the sink implementation. Phase 0 has only github-pr.
	Type string `yaml:"type"`
	// Base is the branch pull requests merge into.
	Base string `yaml:"base"`
	// Label marks pull requests that loomlc opened.
	Label string `yaml:"label"`
	// Reviewer is a GitHub username to request a review from. Empty requests nobody.
	Reviewer string `yaml:"reviewer"`
	Draft    bool   `yaml:"draft"`
	// Template is the pull request template, relative to the checkout.
	Template         string           `yaml:"template"`
	TemplateSections TemplateSections `yaml:"template_sections"`
	Feedback         SinkFeedback     `yaml:"feedback"`
}

// TemplateSections lists the template headings loomlc fills in, matched case-insensitively.
type TemplateSections struct {
	Description []string `yaml:"description"`
	QA          []string `yaml:"qa"`
	Issue       []string `yaml:"issue"`
}

// SinkFeedback configures how review feedback on an open pull request is picked up.
type SinkFeedback struct {
	Label      string `yaml:"label"`
	InProgress string `yaml:"in_progress"`
	// SinceLastReply limits feedback to comments made after loomlc last replied.
	SinceLastReply bool `yaml:"since_last_reply"`
}

// Lifecycle is a named sequence of steps run over a task.
type Lifecycle struct {
	Executor string `yaml:"executor"`
	Source   string `yaml:"source"`
	Sink     string `yaml:"sink"`
	// Concurrency is how many tasks run at once.
	Concurrency int `yaml:"concurrency"`
	// MaxOpenOutputs pauses new pickups while this many outputs, such as pull requests, await review.
	MaxOpenOutputs int      `yaml:"max_open_outputs"`
	WatchInterval  Duration `yaml:"watch_interval"`
	// Branch is a text/template for a task's branch name, with {{.ID}} and {{.Slug}}.
	Branch string `yaml:"branch"`
	// ProtectedPaths are paths a run may not change without a human.
	ProtectedPaths []string          `yaml:"protected_paths"`
	Steps          []Step            `yaml:"steps"`
	Feedback       LifecycleFeedback `yaml:"feedback"`
	Publish        Publish           `yaml:"publish"`
}

// LifecycleFeedback lists the steps a feedback run uses.
type LifecycleFeedback struct {
	Steps []string `yaml:"steps"`
}

// Publish configures the checks that run before results are published.
type Publish struct {
	// Gates must pass before loomlc pushes.
	Gates []Command `yaml:"gates"`
	// BodyGates check the pull request description before it's posted.
	BodyGates []Command `yaml:"body_gates"`
}

// Step is one stage of a lifecycle, run by a provider.
type Step struct {
	Name     string `yaml:"name"`
	Provider string `yaml:"provider"`
	// Vendor is the model vendor, for aggregator providers such as pi.
	Vendor string `yaml:"vendor"`
	// Model is the model to use. Empty means the provider's default.
	Model string `yaml:"model"`
	// Role is a built-in role (planner, engineer, qa) or a path to a prompt file.
	Role string `yaml:"role"`
	// Output is what the step produces: plan, change, or verdict. Built-in roles set it.
	Output   string `yaml:"output"`
	Readonly bool   `yaml:"readonly"`
	// LoopWith names the verdict step this change step repeats with until it passes.
	LoopWith string `yaml:"loop_with"`
	// MaxIter caps how many times a loop runs.
	MaxIter int      `yaml:"max_iter"`
	Timeout Duration `yaml:"timeout"`
	// AllowedCommands are commands the agent may run, for providers with a permission model.
	AllowedCommands []Command `yaml:"allowed_commands"`
	// Gate lists commands loomlc runs before this verdict step; any failure fails the iteration.
	Gate []Command `yaml:"gate"`
}
