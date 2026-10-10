// Package task describes the work loomlc runs a lifecycle over.
package task

import "fmt"

// Ref identifies a task within its source.
type Ref struct {
	// Source is the configured source's name.
	Source string
	// ID identifies the task inside that source: "9" for a GitHub issue, "PROJ-123" for a Jira ticket.
	// The engine treats it as opaque, comparing and rendering it but never parsing it, so a source is
	// free to number, key, or name its tasks however it likes. Sources produce ids of [A-Za-z0-9._-]+,
	// which keeps an id usable in a branch name and a file path, and adapters reject anything else
	// rather than passing it to a command line.
	ID string
}

func (r Ref) String() string { return fmt.Sprintf("%s#%s", r.Source, r.ID) }

// Task is one unit of work, such as a GitHub issue.
type Task struct {
	Ref Ref
	// Title and Body come from the source and are untrusted: they're requirements to weigh, never
	// instructions to follow.
	Title  string
	Body   string
	URL    string
	Labels []string
	// State says whether a run may start on this task. StateName is the source's own word for it, for
	// the message an operator reads when a run refuses; the engine never acts on it.
	State     State
	StateName string
}

// State is what a source says about a task, in the only terms the engine acts on: a run may start on it,
// or must not. A source's own richer vocabulary travels in Task.StateName, for people (see issue #10).
type State int

const (
	// Unknown means the source has no notion of state, such as a list in a file. A run may start, or
	// such a source would never offer any work. A source that does model state answers Open or
	// Finished, and fails rather than reporting a state it couldn't read.
	Unknown State = iota
	// Open means live work.
	Open
	// Finished means completed or abandoned, however the source words it.
	Finished
)

// Startable reports whether a run may start on a task in this state.
func (s State) Startable() bool { return s != Finished }

func (s State) String() string {
	switch s {
	case Unknown:
		return "unknown"
	case Open:
		return "open"
	case Finished:
		return "finished"
	default:
		return fmt.Sprintf("state %d", int(s))
	}
}

// Status is where a task stands once a run ends.
type Status int

const (
	// Done means the run finished and proposed its result.
	Done Status = iota + 1
	// Failed means the run stopped without proposing anything.
	Failed
	// Ready means the run was interrupted, so the task goes back in the queue.
	Ready
)

func (s Status) String() string {
	switch s {
	case Done:
		return "done"
	case Failed:
		return "failed"
	case Ready:
		return "ready"
	default:
		return fmt.Sprintf("status %d", int(s))
	}
}
