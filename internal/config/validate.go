package config

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"
	"unicode"
)

const (
	minWatchInterval  = 30 * time.Second
	maxStepTimeout    = 4 * time.Hour
	maxLoopIterations = 20
)

var (
	namePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	loginPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
)

// problems collects validation errors, each prefixed with the configuration path it's about.
type problems []string

func (p *problems) add(at, format string, args ...any) {
	*p = append(*p, at+": "+fmt.Sprintf(format, args...))
}

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return errors.New("  - " + strings.Join(p, "\n  - "))
}

// validate reports every problem in c at once, so a configuration can be fixed in one pass.
func validate(c *Config) error {
	var p problems
	if c.Version != 1 {
		p.add("version", "must be 1, got %d", c.Version)
	}
	for _, name := range sortedKeys(c.Providers) {
		validateProvider(&p, name, c.Providers[name])
	}
	for _, name := range sortedKeys(c.Executors) {
		validateExecutor(&p, name, c.Executors[name])
	}
	for _, name := range sortedKeys(c.Sources) {
		validateSource(&p, name, c.Sources[name])
	}
	for _, name := range sortedKeys(c.Sinks) {
		validateSink(&p, name, c.Sinks[name])
	}
	for _, name := range sortedKeys(c.Lifecycles) {
		validateLifecycle(&p, c, name, c.Lifecycles[name])
	}
	return p.err()
}

func validateProvider(p *problems, name string, pr Provider) {
	at := "providers." + name
	checkName(p, at, name)
	switch pr.Adapter {
	case AdapterClaude, AdapterCodex, AdapterPi:
	default:
		p.add(at+".adapter", "unknown adapter %q; use claude, codex, or pi", pr.Adapter)
	}
	if strings.ContainsFunc(pr.Cmd, unicode.IsSpace) {
		p.add(at+".cmd", "%q must be a single executable name or path; loomlc passes the arguments itself", pr.Cmd)
	}
	switch {
	case pr.PermissionMode == "":
	case pr.Adapter != AdapterClaude:
		p.add(at+".permission_mode", "only applies to the claude adapter")
	case !isClaudePermissionMode(pr.PermissionMode):
		p.add(at+".permission_mode", "unknown mode %q; use acceptEdits, auto, bypassPermissions, dontAsk, manual, or plan", pr.PermissionMode)
	}
}

// isClaudePermissionMode reports whether mode is one of the modes `claude --help` lists (2.1.269).
func isClaudePermissionMode(mode string) bool {
	switch mode {
	case "acceptEdits", "auto", "bypassPermissions", "dontAsk", "manual", "plan":
		return true
	default:
		return false
	}
}

func validateExecutor(p *problems, name string, e Executor) {
	at := "executors." + name
	checkName(p, at, name)
	switch e.Type {
	case "worktree":
	case "docker", "workshop":
		p.add(at+".type", "the %s executor isn't available until Phase 5; use worktree", e.Type)
	default:
		p.add(at+".type", "unknown executor type %q; use worktree", e.Type)
	}
	checkRelativePath(p, at+".root", e.Root, true)
	if e.Remote == "" {
		p.add(at+".remote", "is required, for example origin")
	}
}

func validateSource(p *problems, name string, s Source) {
	at := "sources." + name
	checkName(p, at, name)
	if s.Type != "github" {
		p.add(at+".type", "unknown source type %q; use github", s.Type)
	}
	if s.Repo != "" && !repoPattern.MatchString(s.Repo) {
		p.add(at+".repo", "%q must be a GitHub repository written as owner/name", s.Repo)
	}
	checkLabels(p, at, map[string]string{
		"trigger.label":      s.Trigger.Label,
		"labels.in_progress": s.Labels.InProgress,
		"labels.done":        s.Labels.Done,
		"labels.failed":      s.Labels.Failed,
	})
}

func validateSink(p *problems, name string, s Sink) {
	at := "sinks." + name
	checkName(p, at, name)
	if s.Type != "github-pr" {
		p.add(at+".type", "unknown sink type %q; use github-pr", s.Type)
	}
	if msg := branchProblem(s.Base); msg != "" {
		p.add(at+".base", "%s", msg)
	}
	if s.Reviewer != "" && !loginPattern.MatchString(s.Reviewer) {
		p.add(at+".reviewer", "%q isn't a valid GitHub username", s.Reviewer)
	}
	checkRelativePath(p, at+".template", s.Template, false)
	checkLabels(p, at, map[string]string{
		"label":                s.Label,
		"feedback.label":       s.Feedback.Label,
		"feedback.in_progress": s.Feedback.InProgress,
	})
}

func validateLifecycle(p *problems, c *Config, name string, lc Lifecycle) {
	at := "lifecycles." + name
	checkName(p, at, name)

	if _, ok := c.Executors[lc.Executor]; !ok {
		p.add(at+".executor", "names executor %q, which isn't defined under executors", lc.Executor)
	}
	source, sourceOK := c.Sources[lc.Source]
	if !sourceOK {
		p.add(at+".source", "names source %q, which isn't defined under sources", lc.Source)
	}
	sink, sinkOK := c.Sinks[lc.Sink]
	if !sinkOK {
		p.add(at+".sink", "names sink %q, which isn't defined under sinks", lc.Sink)
	}
	if sourceOK && sinkOK && sink.Type == "github-pr" && source.Type != "github" {
		p.add(at+".sink", "the github-pr sink needs a github source, so pull requests can close their issues")
	}

	if lc.Concurrency < 1 {
		p.add(at+".concurrency", "must be at least 1")
	}
	if lc.MaxOpenOutputs < 1 {
		p.add(at+".max_open_outputs", "must be at least 1")
	}
	if d := time.Duration(lc.WatchInterval); d < minWatchInterval {
		p.add(at+".watch_interval", "must be at least 30s, got %s", d)
	}
	validateBranchTemplate(p, at+".branch", lc.Branch, sink.Base)
	for i, pp := range lc.ProtectedPaths {
		checkRelativePath(p, fmt.Sprintf("%s.protected_paths[%d]", at, i), pp, true)
	}

	seen := map[string]bool{}
	for i, s := range lc.Steps {
		stepAt := fmt.Sprintf("%s.steps[%d]", at, i)
		validateStep(p, c, stepAt, s)
		if s.Name != "" && seen[s.Name] {
			p.add(stepAt+".name", "step %q is defined more than once", s.Name)
		}
		seen[s.Name] = true
	}
	validateShape(p, at, lc)
}

func validateStep(p *problems, c *Config, at string, s Step) {
	if s.Name == "" {
		p.add(at+".name", "is required")
	} else {
		checkName(p, at+".name", s.Name)
	}

	provider, ok := c.Providers[s.Provider]
	switch {
	case s.Provider == "":
		p.add(at+".provider", "is required")
	case !ok:
		p.add(at+".provider", "names provider %q, which isn't defined under providers", s.Provider)
	case provider.Adapter == AdapterPi:
		if s.Vendor == "" {
			p.add(at+".vendor", "is required for pi, which needs to know which model vendor to call, for example anthropic")
		}
		if s.Model == "" {
			p.add(at+".model", "is required for pi")
		}
	case s.Vendor != "":
		p.add(at+".vendor", "only applies to aggregator providers such as pi; %s calls its own vendor", provider.Adapter)
	}

	validateRole(p, at, s)
	switch {
	case s.Output == OutputChange && s.Readonly:
		p.add(at+".readonly", "a change step edits files, so it can't be readonly")
	case (s.Output == OutputPlan || s.Output == OutputVerdict) && !s.Readonly:
		p.add(at+".readonly", "must be true: only the change step edits files")
	}

	if d := time.Duration(s.Timeout); d <= 0 || d > maxStepTimeout {
		p.add(at+".timeout", "must be more than 0 and at most 4h, got %s", d)
	}
	if len(s.Gate) > 0 && s.Output != OutputVerdict {
		p.add(at+".gate", "gates run just before a verdict step, so only a verdict step can have them")
	}
	switch {
	case s.LoopWith == "" && s.MaxIter != 0:
		p.add(at+".max_iter", "only applies to a step with loop_with")
	case s.LoopWith != "" && (s.MaxIter < 1 || s.MaxIter > maxLoopIterations):
		p.add(at+".max_iter", "must be between 1 and 20, got %d", s.MaxIter)
	}
}

func validateRole(p *problems, at string, s Step) {
	switch builtin := roleOutput(s.Role); {
	case s.Role == "":
		p.add(at+".role", "is required: planner, engineer, qa, or a path to a .md prompt")
	case builtin != "":
		if s.Output != builtin {
			p.add(at+".output", "the %s role produces a %s, not a %s", s.Role, builtin, s.Output)
		}
	case strings.HasSuffix(s.Role, ".md"):
		checkRelativePath(p, at+".role", s.Role, true)
		if s.Output != OutputPlan && s.Output != OutputChange && s.Output != OutputVerdict {
			p.add(at+".output", "is required for a custom role: plan, change, or verdict")
		}
	default:
		p.add(at+".role", "unknown role %q; use planner, engineer, qa, or a path to a .md prompt", s.Role)
	}
}

// validateShape checks the Phase 0 lifecycle shape: a plan step, then a change step that loops with the
// verdict step after it, and feedback runs that use that loop.
func validateShape(p *problems, at string, lc Lifecycle) {
	const shape = "Phase 0 lifecycles need exactly three steps in this order: a plan step, a change step with loop_with, and the verdict step it loops with"
	if len(lc.Steps) != 3 {
		p.add(at+".steps", "%s; found %d steps", shape, len(lc.Steps))
		return
	}
	plan, change, verdict := lc.Steps[0], lc.Steps[1], lc.Steps[2]
	if plan.Output != OutputPlan || change.Output != OutputChange || verdict.Output != OutputVerdict || change.LoopWith != verdict.Name {
		p.add(at+".steps", "%s", shape)
	}
	if plan.LoopWith != "" || verdict.LoopWith != "" {
		p.add(at+".steps", "only the change step can use loop_with")
	}
	if want := []string{change.Name, verdict.Name}; !slices.Equal(lc.Feedback.Steps, want) {
		p.add(at+".feedback.steps", "must be [%s, %s]: feedback runs repeat the change step and the verdict step it loops with", change.Name, verdict.Name)
	}
}

// validateBranchTemplate checks that the template renders a valid branch name, different for every
// task, and never the base branch.
func validateBranchTemplate(p *problems, at, tmpl, base string) {
	if tmpl == "" {
		p.add(at, "is required, for example %q", "feat/issue-{{.ID}}-{{.Slug}}")
		return
	}
	first, err := renderBranch(tmpl, "1", "first-task")
	if err != nil {
		p.add(at, "%v", err)
		return
	}
	second, err := renderBranch(tmpl, "2", "second-task")
	if err != nil {
		p.add(at, "%v", err)
		return
	}
	switch {
	case first == second:
		p.add(at, "must use {{.ID}}, so each task gets its own branch")
	case branchProblem(first) != "":
		p.add(at, "renders %q: %s", first, branchProblem(first))
	case first == base || second == base:
		p.add(at, "must never render the base branch %q", base)
	}
}

// branchData is what a branch template can refer to.
type branchData struct {
	ID   string // the task's id in its source, such as a GitHub issue number
	Slug string // the task title in lowercase, with other characters replaced by hyphens
}

func renderBranch(tmpl string, id, slug string) (string, error) {
	t, err := template.New("branch").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("invalid template: %w", err)
	}
	var b strings.Builder
	if err := t.Execute(&b, branchData{ID: id, Slug: slug}); err != nil {
		return "", fmt.Errorf("can't render: %w; use {{.ID}} and {{.Slug}}", err)
	}
	return b.String(), nil
}

// branchProblem explains why name isn't a usable git branch name, or returns "".
func branchProblem(name string) string {
	switch {
	case name == "":
		return "is required, for example main"
	case strings.ContainsFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }),
		strings.ContainsAny(name, "~^:?*[\\"),
		strings.Contains(name, ".."),
		strings.Contains(name, "@{"),
		strings.Contains(name, "//"),
		strings.HasPrefix(name, "/"), strings.HasPrefix(name, "-"),
		strings.HasSuffix(name, "/"), strings.HasSuffix(name, "."), strings.HasSuffix(name, ".lock"):
		return fmt.Sprintf("%q isn't a valid git branch name", name)
	default:
		return ""
	}
}

// checkName reports a configuration name that isn't lowercase letters, digits, and hyphens.
func checkName(p *problems, at, name string) {
	if !namePattern.MatchString(name) {
		p.add(at, "name %q must start with a lowercase letter and use only lowercase letters, digits, and hyphens", name)
	}
}

// checkLabels reports missing labels and labels used for more than one purpose. labels maps each
// setting's path, relative to at, to its value.
func checkLabels(p *problems, at string, labels map[string]string) {
	usedBy := map[string]string{}
	for _, key := range sortedKeys(labels) {
		value := labels[key]
		if value == "" {
			p.add(at+"."+key, "is required")
			continue
		}
		if other, ok := usedBy[value]; ok {
			p.add(at+"."+key, "label %q is already used by %s; each setting needs its own label", value, other)
			continue
		}
		usedBy[value] = key
	}
}

// checkRelativePath reports a path that isn't relative to the repository or that escapes it.
func checkRelativePath(p *problems, at, value string, required bool) {
	switch {
	case value == "":
		if required {
			p.add(at, "is required")
		}
	case path.IsAbs(value) || filepath.IsAbs(value):
		p.add(at, "%q must be relative to the repository", value)
	case slices.Contains(strings.Split(path.Clean(filepath.ToSlash(value)), "/"), ".."):
		p.add(at, "%q must stay inside the repository", value)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
