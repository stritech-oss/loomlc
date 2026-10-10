package app

import (
	"context"
	"fmt"
	"slices"
)

// Preflight reports what stops this lifecycle running, before a task is claimed. Everything it finds is
// something an operator fixes once: a missing CLI, a repository loomlc can't reach, a lifecycle that
// checks nothing.
//
// It deliberately asks the local machine only. Whether the forge has the labels is a question for
// `loomlc doctor`, which may use the network.
func (a *App) Preflight(ctx context.Context, lc *Lifecycle) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if !lc.Verified && !lc.Config.Publish.AllowUnverified {
		add("nothing in %s builds or tests the work: give a step a gate, add publish gates, or set lifecycles.%s.publish.allow_unverified", lc.Name, lc.Name)
	}

	for _, cmd := range a.commands(lc) {
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

// commands lists the executables a run needs: git, the forge's CLI, and each step's provider.
func (a *App) commands(lc *Lifecycle) []string {
	cmds := []string{"git", "gh"}
	for _, s := range lc.Config.Steps {
		if cmd := a.cfg.Providers[s.Provider].Cmd; cmd != "" && !slices.Contains(cmds, cmd) {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}
