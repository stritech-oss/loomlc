// Package executor defines workspaces, where steps run, and the contract executors implement to provide
// them.
package executor

import "fmt"

// Isolation says how strongly a workspace separates a run from the operator's machine.
type Isolation int

const (
	// None means the run shares the operator's user account, files, network, and credentials, as with a
	// git worktree.
	None Isolation = iota + 1
	// Container means the run is confined, as with the docker executor planned for Phase 5.
	Container
)

func (i Isolation) String() string {
	switch i {
	case None:
		return "none"
	case Container:
		return "container"
	default:
		return fmt.Sprintf("isolation %d", int(i))
	}
}

// Mode says where a workspace's branch starts.
type Mode int

const (
	// NewBranch starts the branch from the base branch, replacing any local branch with the same name.
	NewBranch Mode = iota + 1
	// ExistingBranch checks out a branch that already exists on the remote, such as a pull request's.
	ExistingBranch
)

// Spec describes the workspace a run needs.
type Spec struct {
	// Key names the workspace and is unique per task, such as "issue-42".
	Key string
	// RunID identifies the run. It names the backup of work from an earlier attempt.
	RunID string
	// Branch is the branch the run works on.
	Branch string
	// Base is the branch the work merges into, such as "main".
	Base string
	Mode Mode
}

// Workspace is a prepared workspace.
type Workspace struct {
	Key string
	// Dir is the workspace's absolute path.
	Dir    string
	Branch string
	// BaseRef is the remote-tracking ref of the base branch, such as "origin/main".
	BaseRef string
	// BaseSHA is the commit the run's own work starts after: BaseRef for a new branch, or the point where an
	// existing branch forked from it.
	BaseSHA   string
	Isolation Isolation
	// Backup is the ref that saved commits from an earlier attempt when Prepare replaced the branch, or "".
	Backup string
}
