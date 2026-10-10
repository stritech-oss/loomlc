package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stritech-oss/loomlc/internal/app"
	"github.com/stritech-oss/loomlc/internal/run"
	"github.com/stritech-oss/loomlc/internal/task"
)

const runUsage = `Usage: loomlc run [flags] [task]

Runs one task through a lifecycle: it plans, changes, reviews, and when the review passes it
opens a pull request. Without a task it takes the next ready one, and takes none while too many
outputs are already open. Naming a task runs that task, whatever its labels say and however much
is open; the task is named by its id in the lifecycle's source, such as 9 for a GitHub issue.

Exit codes:
  0  a pull request was opened, or there was nothing to pick up
  1  nothing was proposed — a step stopped, the review never passed, publishing was refused,
     or the run itself failed. Either way the reasons are on the task or on stderr.
  2  the command line was wrong

`

// taskID is what a source may call a task, and so what an operator may type. It is checked here rather
// than passed on, because it ends up in a command line, a branch name, and a path.
var taskID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func runRun(ctx context.Context, args []string, env Env) int {
	flags := flag.NewFlagSet("loomlc run", flag.ContinueOnError)
	flags.SetOutput(env.Stderr)
	// flag prints its own copy of the usage on any error, including help that was asked for. Help goes
	// to stdout below, where it can be paged, and an error is reported by this command.
	flags.Usage = func() {}
	usage := func(w io.Writer) int {
		_, _ = io.WriteString(w, runUsage)
		flags.SetOutput(w)
		flags.PrintDefaults()
		return ExitOK
	}
	configPath := flags.String("config", "", "read the configuration from `path` instead of ./"+defaultConfigFile)
	name := flags.String("lifecycle", "", "run the lifecycle called `name`, when the configuration has more than one")
	maxIter := flags.Int("max-iter", 0, "allow `n` passes of the change step, instead of the lifecycle's own limit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return usage(env.Stdout)
		}
		return ExitUsage
	}
	if flags.NArg() > 1 {
		return fail(env.Stderr, fmt.Sprintf("loomlc run: one task at a time, and %q is more than one\n", flags.Args()))
	}
	if err := checkConfigFlag(flags); err != nil {
		return fail(env.Stderr, fmt.Sprintf("loomlc run: %v\n", err))
	}
	// A task can be given as it is printed — github#9 — as well as by its bare id.
	source, id, printed := strings.Cut(flags.Arg(0), "#")
	if !printed {
		source, id = "", source
	}
	if id != "" && !taskID.MatchString(id) {
		return fail(env.Stderr, fmt.Sprintf("loomlc run: %q isn't a task id: letters, digits, dots, hyphens and underscores\n", id))
	}
	if *maxIter < 0 {
		return fail(env.Stderr, "loomlc run: --max-iter must be at least 1, or left out to use the lifecycle's own limit\n")
	}

	engine, lifecycleSource, code := build(ctx, env, *configPath, *name, *maxIter)
	if code != ExitOK {
		return code
	}
	if source != "" && source != lifecycleSource {
		return fail(env.Stderr, fmt.Sprintf("loomlc run: this lifecycle takes its tasks from %q, not %q\n", lifecycleSource, source))
	}

	ref := task.Ref{}
	if id != "" {
		ref = task.Ref{Source: lifecycleSource, ID: id}
	}
	report, err := engine.Run(ctx, ref)
	// A failed write to stdout can't be reported anywhere useful, and the exit code has to describe the
	// run rather than the pipe.
	_, _ = io.WriteString(env.Stdout, describeRun(report))
	if err != nil {
		return failure(env.Stderr, fmt.Sprintf("loomlc run: %v\n", err))
	}
	if report.Idle == "" && !report.Published {
		return ExitFailure // the reasons are on the task, and in what was just printed
	}
	return ExitOK
}

// build turns the configuration into the engine that runs a task, and reports what would stop it before
// any task is claimed. It returns the source's name, because that is what a task id typed on the command
// line belongs to.
func build(ctx context.Context, env Env, configPath, name string, maxIter int) (*run.Engine, string, int) {
	cfg, _, err := loadConfig(env, configPath)
	if err != nil {
		return nil, "", failure(env.Stderr, fmt.Sprintf("loomlc run: %v\n", err))
	}
	dir, err := filepath.Abs(env.Dir)
	if err != nil {
		return nil, "", failure(env.Stderr, fmt.Sprintf("loomlc run: %v\n", err))
	}
	a, err := app.New(app.Options{
		Config:   cfg,
		Repo:     dir,
		Env:      env.Environ(),
		Runner:   env.Runner,
		ReadFile: readIn(dir, env.ReadFile),
	})
	if err != nil {
		return nil, "", failure(env.Stderr, fmt.Sprintf("loomlc run: %v\n", err))
	}
	lc, err := a.Lifecycle(name)
	if err != nil {
		return nil, "", failure(env.Stderr, fmt.Sprintf("loomlc run: %v\n", err))
	}
	if problems := a.Preflight(ctx, lc); len(problems) > 0 {
		return nil, "", failure(env.Stderr, fmt.Sprintf("loomlc run: %s can't run yet:\n%s", lc.Name, bullets(problems)))
	}
	engine, err := a.Engine(lc, app.EngineOptions{MaxPasses: maxIter, Progress: env.Stdout})
	if err != nil {
		return nil, "", failure(env.Stderr, fmt.Sprintf("loomlc run: %v\n", err))
	}
	return engine, lc.Config.Source, ExitOK
}

// describeRun says how the run ended, after the lines it printed as it went.
func describeRun(report run.Report) string {
	switch {
	case report.Idle != "":
		return "Nothing to do: " + report.Idle + ".\n"
	case report.Published:
		var b strings.Builder
		fmt.Fprintf(&b, "Proposed in %s\n", report.Output.URL)
		for _, w := range report.Warnings {
			fmt.Fprintf(&b, "  warning: %s\n", w)
		}
		return b.String()
	case !report.Claimed:
		return "" // nothing was taken, and the error says why
	case !report.Settled:
		// The run didn't reach a judgement, so nothing was said on the task and nothing was published.
		return fmt.Sprintf("%s went back in the queue; nothing was said on it.\n", report.Task)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Nothing was proposed for %s.\n", report.Task)
	if len(report.Blockers) > 0 {
		b.WriteString("What stopped it:\n" + bullets(report.Blockers))
	}
	if len(report.Refusals) > 0 {
		b.WriteString("Why it wasn't published:\n" + bullets(report.Refusals))
	}
	if report.Log != "" {
		fmt.Fprintf(&b, "The run's prompts and answers are in %s\n", report.Log)
	}
	return b.String()
}

// bullets lists problems one per line, indenting the lines of a problem that has several, such as a
// gate's output.
func bullets(items []string) string {
	var b strings.Builder
	for _, item := range items {
		lines := strings.Split(strings.TrimRight(item, "\n"), "\n")
		fmt.Fprintf(&b, "  - %s\n", lines[0])
		for _, line := range lines[1:] {
			fmt.Fprintf(&b, "    %s\n", line)
		}
	}
	return b.String()
}

// readIn resolves a configuration's relative paths against the checkout, so a run reads the same prompt
// files and templates wherever it was started from.
func readIn(dir string, read func(string) ([]byte, error)) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		if filepath.IsAbs(name) {
			return read(name)
		}
		return read(filepath.Join(dir, name))
	}
}
