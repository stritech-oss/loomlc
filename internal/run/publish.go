// Package run decides whether a run's work can be published, and prepares it.
package run

import (
	"context"
	"fmt"
	"strings"

	"github.com/stritech-oss/loomlc/internal/gate"
	"github.com/stritech-oss/loomlc/internal/git"
)

// VCS is what publishing needs from git.
type VCS interface {
	Autosquash(ctx context.Context, dir, base string) error
	Log(ctx context.Context, dir, base string) ([]git.Commit, error)
	PathsSince(ctx context.Context, dir, base string) ([]string, error)
	Diff(ctx context.Context, dir, base string) (string, error)
}

// Checks runs the lifecycle's publish gates.
type Checks interface {
	Run(ctx context.Context, dir string, commands [][]string) ([]gate.Result, error)
}

// Policy is the commit policy checker, read from the operator's checkout rather than the workspace: an
// agent can edit the workspace, and this is the check that says what it may commit.
type Policy interface {
	CheckRange(ctx context.Context, dir, base, head string) error
}

// Scanner reports credential-shaped text in a diff.
type Scanner interface {
	Scan(diff string) []Match
}

// Options configure a publish decision.
type Options struct {
	// Dir is the workspace, Base the commit the run's work starts after, Branch its branch.
	Dir, Base, Branch string
	// ProtectedPaths are paths a run may not change.
	ProtectedPaths []string
	// Gates must pass before a push.
	Gates [][]string
	// Verified says the lifecycle has checks that build or test the work.
	Verified bool
	// AllowUnverified lets an unverified lifecycle publish anyway, saying so.
	AllowUnverified bool
}

// Decision is whether a run's work can be published.
type Decision struct {
	// Refusals are the reasons it can't be. Empty means it can.
	Refusals []string
	// Warnings are what a reader of the pull request has to be told.
	Warnings []string
	// Commits are the commits to publish, after fixups were folded.
	Commits []git.Commit
	// Gates are the results of the publish gates.
	Gates []gate.Result
}

// OK reports whether the work can be published.
func (d Decision) OK() bool { return len(d.Refusals) == 0 }

// Publisher decides whether work can be published.
type Publisher struct {
	vcs     VCS
	checks  Checks
	policy  Policy
	scanner Scanner
}

// New returns a Publisher. scanner may be nil, which skips the credential scan.
func New(vcs VCS, checks Checks, policy Policy, scanner Scanner) *Publisher {
	return &Publisher{vcs: vcs, checks: checks, policy: policy, scanner: scanner}
}

// Prepare folds the fixups a run made itself and checks the work can be published. It stops at the first
// refusal that makes the later checks meaningless, and otherwise reports everything it found at once, so
// one run doesn't hide the next problem.
func (p *Publisher) Prepare(ctx context.Context, opts Options) (Decision, error) {
	var d Decision

	// Nothing that follows means anything if the work was never built or tested.
	if !opts.Verified {
		if !opts.AllowUnverified {
			d.Refusals = append(d.Refusals, "nothing in this lifecycle builds or tests the work: give the review step a gate, add publish gates, or set publish.allow_unverified")
			return d, nil
		}
		d.Warnings = append(d.Warnings, "No checks ran: nothing built or tested this change.")
	}

	// Folding first, so every check below reads the history that will be pushed.
	if err := p.vcs.Autosquash(ctx, opts.Dir, opts.Base); err != nil {
		d.Refusals = append(d.Refusals, fmt.Sprintf("the fixes this run made couldn't be folded in: %v", err))
		return d, nil
	}
	commits, err := p.vcs.Log(ctx, opts.Dir, opts.Base)
	if err != nil {
		return d, err
	}
	d.Commits = commits
	if len(commits) == 0 {
		d.Refusals = append(d.Refusals, "there is nothing to publish: no commits on the branch")
		return d, nil
	}

	paths, err := p.vcs.PathsSince(ctx, opts.Dir, opts.Base)
	if err != nil {
		return d, err
	}
	for _, path := range protectedChanges(paths, opts.ProtectedPaths) {
		d.Refusals = append(d.Refusals, fmt.Sprintf("%s is protected and this change modifies it", path))
	}

	if p.scanner != nil {
		diff, err := p.vcs.Diff(ctx, opts.Dir, opts.Base)
		if err != nil {
			return d, err
		}
		for _, m := range p.scanner.Scan(diff) {
			d.Refusals = append(d.Refusals, fmt.Sprintf("%s:%d adds %s; loomlc won't publish it", m.Path, m.Line, m.Rule))
		}
	}

	if err := p.policy.CheckRange(ctx, opts.Dir, opts.Base, "HEAD"); err != nil {
		d.Refusals = append(d.Refusals, fmt.Sprintf("the commits don't pass the commit policy: %v", err))
	}

	gates, err := p.checks.Run(ctx, opts.Dir, opts.Gates)
	if err != nil {
		return d, err
	}
	d.Gates = gates
	for _, failed := range gate.Failed(gates) {
		d.Refusals = append(d.Refusals, fmt.Sprintf("the publish gate `%s` failed:\n%s", failed.Name, strings.TrimSpace(failed.Output)))
	}
	return d, nil
}

// protectedChanges returns the changed paths that a protected path covers. A protected directory covers
// everything under it.
func protectedChanges(changed, protected []string) []string {
	var hits []string
	for _, path := range changed {
		for _, p := range protected {
			if path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") {
				hits = append(hits, path)
				break
			}
		}
	}
	return hits
}
