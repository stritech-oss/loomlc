// Package app builds the parts of a run from a configuration. It wires; it decides nothing.
package app

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/stritech-oss/loomlc/internal/config"
	"github.com/stritech-oss/loomlc/internal/executor/worktree"
	"github.com/stritech-oss/loomlc/internal/forge/github"
	"github.com/stritech-oss/loomlc/internal/gate"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/lifecycle"
	"github.com/stritech-oss/loomlc/internal/policy"
	"github.com/stritech-oss/loomlc/internal/proc"
	"github.com/stritech-oss/loomlc/internal/prompt"
	"github.com/stritech-oss/loomlc/internal/provider/claude"
	"github.com/stritech-oss/loomlc/internal/redact"
	"github.com/stritech-oss/loomlc/internal/run"
	"github.com/stritech-oss/loomlc/internal/runlog"
)

// policyScript is where loomlc looks for the commit policy checker in the operator's checkout, and
// runRoot where it writes what each run did. Neither is configurable in Phase 0.
const (
	policyScript = "scripts/commit-policy.sh"
	runRoot      = ".loomlc/runs"
)

// Options configure an App.
type Options struct {
	// Config is the resolved configuration.
	Config *config.Config
	// Repo is the operator's checkout, as an absolute path.
	Repo string
	// Env is the environment the tools run with: whatever git, gh and the agent CLIs need.
	Env []string
	// Runner runs the subprocesses. Tests replace it.
	Runner proc.Runner
	// ReadFile reads a file from the checkout, for a lifecycle's own prompt files.
	ReadFile func(name string) ([]byte, error)
}

// App builds a run's parts.
type App struct {
	cfg      *config.Config
	repo     string
	env      []string
	runner   proc.Runner
	readFile func(string) ([]byte, error)
	// secrets masks the values in Env wherever loomlc writes what a tool printed.
	secrets *redact.Redactor
	git     *git.Client
}

// New returns an App for a configuration.
func New(opts Options) (*App, error) {
	switch {
	case opts.Config == nil:
		return nil, fmt.Errorf("app: no configuration")
	case !filepath.IsAbs(opts.Repo):
		return nil, fmt.Errorf("app: the repository path %q must be absolute", opts.Repo)
	case opts.Runner == nil:
		return nil, fmt.Errorf("app: no process runner")
	}
	return &App{
		cfg:      opts.Config,
		repo:     filepath.Clean(opts.Repo),
		env:      slices.Clone(opts.Env),
		runner:   opts.Runner,
		readFile: opts.ReadFile,
		secrets:  redact.New(redact.SecretValues(opts.Env)...),
		git:      git.New(opts.Runner, opts.Env),
	}, nil
}

// Lifecycle is everything one lifecycle needs to run a task.
type Lifecycle struct {
	Name   string
	Config config.Lifecycle
	// Source and Sink are the same forge: tasks come from its issues and results go to its pull requests.
	Forge     *github.Forge
	Executor  *worktree.Executor
	Checks    *gate.Runner
	Policy    *policy.Checker
	Steps     []lifecycle.Step
	Summary   []run.Step
	Verified  bool
	Templates run.Output
}

// Lifecycle builds the named lifecycle, or the only one when name is empty.
func (a *App) Lifecycle(name string) (*Lifecycle, error) {
	name, lc, err := a.pick(name)
	if err != nil {
		return nil, err
	}

	forge, err := a.forge(lc)
	if err != nil {
		return nil, err
	}
	executor, err := worktree.New(worktree.Options{
		Repo:   a.repo,
		Root:   a.cfg.Executors[lc.Executor].Root,
		Remote: a.cfg.Executors[lc.Executor].Remote,
		Env:    a.env,
	}, a.runner)
	if err != nil {
		return nil, fmt.Errorf("lifecycle %s: %w", name, err)
	}
	checker, err := policy.New(filepath.Join(a.repo, policyScript), a.runner, a.env)
	if err != nil {
		return nil, fmt.Errorf("lifecycle %s: loomlc needs %s in the repository: %w", name, policyScript, err)
	}
	steps, summary, err := a.steps(lc)
	if err != nil {
		return nil, fmt.Errorf("lifecycle %s: %w", name, err)
	}
	template, err := a.template(lc)
	if err != nil {
		return nil, fmt.Errorf("lifecycle %s: %w", name, err)
	}

	return &Lifecycle{
		Name:      name,
		Config:    lc,
		Forge:     forge,
		Executor:  executor,
		Checks:    gate.New(gate.Options{Env: a.env, Redact: a.secrets}, a.runner),
		Policy:    checker,
		Steps:     steps,
		Summary:   summary,
		Verified:  verified(lc),
		Templates: template,
	}, nil
}

// Git is the client the engine commits through.
func (a *App) Git() *git.Client { return a.git }

// Secrets masks the values this run was given, for anything loomlc writes down.
func (a *App) Secrets() *redact.Redactor { return a.secrets }

// Scanner refuses a diff that adds a credential, exempting the paths a lifecycle protects: a protected
// path is refused anyway, and a repository's own fixtures are the usual reason to exempt one.
func (a *App) Scanner(lc config.Lifecycle) *run.Secrets {
	return run.NewSecrets(a.env, lc.ProtectedPaths)
}

// pick resolves a lifecycle by name, or the only one there is.
func (a *App) pick(name string) (string, config.Lifecycle, error) {
	if name != "" {
		lc, ok := a.cfg.Lifecycles[name]
		if !ok {
			return "", config.Lifecycle{}, fmt.Errorf("no lifecycle named %q; the configuration has %s", name, strings.Join(names(a.cfg), ", "))
		}
		return name, lc, nil
	}
	if len(a.cfg.Lifecycles) != 1 {
		return "", config.Lifecycle{}, fmt.Errorf("the configuration has %d lifecycles (%s); name the one to run", len(a.cfg.Lifecycles), strings.Join(names(a.cfg), ", "))
	}
	for name, lc := range a.cfg.Lifecycles {
		return name, lc, nil
	}
	return "", config.Lifecycle{}, fmt.Errorf("the configuration has no lifecycles")
}

// forge builds the GitHub source and sink, which are one adapter.
func (a *App) forge(lc config.Lifecycle) (*github.Forge, error) {
	source, sink := a.cfg.Sources[lc.Source], a.cfg.Sinks[lc.Sink]
	repo := source.Repo
	if repo == "" {
		return nil, fmt.Errorf("sources.%s.repo is empty and loomlc can't yet read the repository from the checkout; set it to owner/name", lc.Source)
	}
	return github.New(github.Options{
		SourceName:     lc.Source,
		SinkName:       lc.Sink,
		Repo:           repo,
		Env:            a.env,
		Trigger:        source.Trigger.Label,
		Labels:         github.Labels{InProgress: source.Labels.InProgress, Done: source.Labels.Done, Failed: source.Labels.Failed},
		OutputLabel:    sink.Label,
		Feedback:       github.FeedbackLabels{Ready: sink.Feedback.Label, InProgress: sink.Feedback.InProgress},
		SinceLastReply: sink.Feedback.SinceLastReply,
		FeedbackFrom:   sink.Feedback.From,
	}, a.runner)
}

// steps builds each step's provider and role, and the list the summary comment reads.
func (a *App) steps(lc config.Lifecycle) ([]lifecycle.Step, []run.Step, error) {
	loader := prompt.Loader{ReadFile: a.readFile}
	var steps []lifecycle.Step
	var summary []run.Step

	for _, s := range lc.Steps {
		agent, err := a.agent(s)
		if err != nil {
			return nil, nil, err
		}
		role, err := loader.Role(s.Role)
		if err != nil {
			return nil, nil, fmt.Errorf("step %s: %w", s.Name, err)
		}
		steps = append(steps, lifecycle.Step{
			Name:    s.Name,
			Output:  s.Output,
			Role:    role,
			Agent:   agent,
			Model:   s.Model,
			Vendor:  s.Vendor,
			Allowed: argvs(s.AllowedCommands),
			Gate:    argvs(s.Gate),
			Timeout: time.Duration(s.Timeout),
		})
		summary = append(summary, run.Step{Name: s.Name, Provider: s.Provider, Model: s.Model, Vendor: s.Vendor})
	}
	return steps, summary, nil
}

// agent builds the provider adapter a step names.
func (a *App) agent(s config.Step) (lifecycle.Agent, error) {
	p := a.cfg.Providers[s.Provider]
	switch p.Adapter {
	case config.AdapterClaude:
		return claude.New(claude.Options{Name: s.Provider, Cmd: p.Cmd, PermissionMode: p.PermissionMode}, a.runner), nil
	case config.AdapterCodex, config.AdapterPi:
		return nil, fmt.Errorf("step %s: the %s adapter isn't written yet; it arrives later in Phase 0", s.Name, p.Adapter)
	default:
		return nil, fmt.Errorf("step %s: unknown provider adapter %q", s.Name, p.Adapter)
	}
}

// template reads the repository's pull request template and the headings to fill.
func (a *App) template(lc config.Lifecycle) (run.Output, error) {
	sink := a.cfg.Sinks[lc.Sink]
	out := run.Output{
		Remote:   a.cfg.Executors[lc.Executor].Remote,
		Base:     sink.Base,
		Labels:   []string{sink.Label},
		Reviewer: sink.Reviewer,
		Draft:    sink.Draft,
		Sections: run.Sections{
			Description: sink.TemplateSections.Description,
			QA:          sink.TemplateSections.QA,
			Issue:       sink.TemplateSections.Issue,
		},
	}
	if sink.Template == "" {
		return out, fmt.Errorf("sinks.%s.template is empty; loomlc fills the repository's own template", lc.Sink)
	}
	if a.readFile == nil {
		return out, fmt.Errorf("no way to read %s", sink.Template)
	}
	body, err := a.readFile(sink.Template)
	if err != nil {
		return out, fmt.Errorf("read the pull request template: %w", err)
	}
	out.Template = string(body)
	return out, nil
}

// verified reports whether anything in the lifecycle builds or tests the work.
func verified(lc config.Lifecycle) bool {
	if len(lc.Publish.Gates) > 0 {
		return true
	}
	return slices.ContainsFunc(lc.Steps, func(s config.Step) bool { return len(s.Gate) > 0 })
}

// argvs converts configured commands into argument vectors.
func argvs(cmds []config.Command) [][]string {
	out := make([][]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, c)
	}
	return out
}

func names(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Lifecycles))
	for name := range cfg.Lifecycles {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// EngineOptions are what the command line decides about a run, rather than the configuration.
type EngineOptions struct {
	// MaxPasses caps the change-and-judge loop instead of the change step's own max_iter. Zero keeps it.
	MaxPasses int
	// Progress receives a line as the run goes. Nil is silent.
	Progress io.Writer
}

// Engine returns the engine that takes one task through this lifecycle.
func (a *App) Engine(lc *Lifecycle, opts EngineOptions) (*run.Engine, error) {
	// Configuration validation guarantees the three steps and their order; this is the assertion, since
	// the run indexes them.
	if len(lc.Steps) != 3 {
		return nil, fmt.Errorf("lifecycle %s: %d steps, want three", lc.Name, len(lc.Steps))
	}
	passes := lc.Config.Steps[1].MaxIter
	if opts.MaxPasses > 0 {
		passes = opts.MaxPasses
	}

	output := lc.Templates
	output.Steps = lc.Summary
	branch := lc.Config.Branch

	return &run.Engine{
		Lifecycle: lifecycle.Spec{
			Name:           lc.Name,
			Plan:           lc.Steps[0],
			Change:         lc.Steps[1],
			Verdict:        lc.Steps[2],
			MaxPasses:      passes,
			ProtectedPaths: lc.Config.ProtectedPaths,
		},
		Publishing: run.Options{
			ProtectedPaths:  lc.Config.ProtectedPaths,
			Gates:           argvs(lc.Config.Publish.Gates),
			Verified:        lc.Verified,
			AllowUnverified: lc.Config.Publish.AllowUnverified,
		},
		Output:     output,
		Source:     lc.Forge,
		Workspaces: lc.Executor,
		Steps:      lifecycle.New(a.git, lc.Checks, opts.Progress),
		Publisher:  run.New(a.git, lc.Checks, lc.Policy, a.Scanner(lc.Config)),
		Opener:     run.NewOpener(a.git, lc.Forge, lc.Forge, lc.Policy),
		Policy:     lc.Policy,
		Branch: func(id, title string) (string, error) {
			return config.BranchName(branch, id, title)
		},
		MaxOpenOutputs: lc.Config.MaxOpenOutputs,
		NewLog: func(id string) (run.Log, error) {
			return runlog.New(filepath.Join(a.repo, runRoot), id, a.secrets)
		},
		Progress: opts.Progress,
	}, nil
}
