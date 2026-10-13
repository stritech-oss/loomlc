package app

import (
	"context"
	"fmt"
	"slices"
)

// Needs says how much of the machine a command uses, since not every command runs agents.
type Needs int

const (
	// RunsAgents means the command takes work through the lifecycle's steps: every provider's CLI has
	// to be installed, and something has to build or test what they write.
	RunsAgents Needs = iota + 1
	// MovesCommits means the command only rearranges commits and talks to the forge, as tidy does. A
	// provider that isn't installed can't stop it, and neither can a lifecycle that checks nothing.
	MovesCommits
)

// Everything below asks whether the command is MovesCommits rather than whether it is RunsAgents, so a
// caller that passes no Needs at all gets every check rather than the fewest.

// Preflight reports what stops this lifecycle running the command needs describes, before a task is
// claimed. Everything it finds is something an operator fixes once: a missing CLI, a repository loomlc
// can't reach, a lifecycle that checks nothing.
//
// It deliberately asks the local machine only. Whether the forge has the labels is a question for
// `loomlc doctor`, which may use the network.
func (a *App) Preflight(ctx context.Context, lc *Lifecycle, needs Needs) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if needs != MovesCommits && !lc.Verified && !lc.Config.Publish.AllowUnverified {
		add("nothing in %s builds or tests the work: give a step a gate, add publish gates, or set lifecycles.%s.publish.allow_unverified", lc.Name, lc.Name)
	}

	for _, cmd := range a.commands(lc, needs) {
		if _, err := a.runner.LookPath(cmd); err != nil {
			add("%s isn't on PATH: %v", cmd, err)
		}
	}

	root, err := a.git.Run(ctx, a.repo, "rev-parse", "--show-toplevel")
	if err != nil {
		add("%s isn't a git repository: %v", a.repo, err)
		return problems // everything below asks git about this repository
	}
	// A run writes its workspaces and records beside the checkout and reads its policy script from it, so
	// started from a subdirectory it would use paths nobody meant.
	if root != a.repo {
		add("run loomlc from the repository's root, %s, not %s", root, a.repo)
	}
	remote := a.cfg.Executors[lc.Config.Executor].Remote
	if _, err := a.git.Run(ctx, a.repo, "remote", "get-url", remote); err != nil {
		add("the remote %q isn't configured in %s: %v", remote, a.repo, err)
	}
	return problems
}

// commands lists the executables the command needs: git, the forge's CLI, and, for a command that runs
// agents, each step's provider.
func (a *App) commands(lc *Lifecycle, needs Needs) []string {
	cmds := []string{"git", "gh"}
	if needs == MovesCommits {
		return cmds
	}
	for _, s := range lc.Config.Steps {
		if cmd := a.cfg.Providers[s.Provider].Cmd; cmd != "" && !slices.Contains(cmds, cmd) {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}
