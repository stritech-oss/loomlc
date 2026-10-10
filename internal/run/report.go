package run

import (
	"fmt"
	"strings"

	"github.com/stritech-oss/loomlc/internal/lifecycle"
	"github.com/stritech-oss/loomlc/internal/sink"
)

// Problem is why a run proposed nothing, for the comment it leaves on the task.
type Problem struct {
	Outcome lifecycle.Outcome
	Passes  int
	// Blockers are what a step said stopped it; Refusals why work that passed review wasn't published.
	Blockers, Refusals []string
	// Log is where the run's record is, so a person can read the prompts and the answers.
	Log string
}

// Failure is the comment loomlc leaves on a task it couldn't finish: what stopped the run, in the words
// of whatever stopped, and where to read the rest.
//
// It carries loomlc's marker, so a later run reads it as loomlc's own and not as feedback to act on.
func Failure(p Problem) string {
	var b strings.Builder
	b.WriteString(sink.Marker + "failed -->\n")
	b.WriteString("## loomlc stopped without proposing anything\n\n")
	b.WriteString(reason(p) + "\n")

	writeItems(&b, "What stopped it", p.Blockers)
	writeItems(&b, "Why it wasn't published", p.Refusals)
	if p.Log != "" {
		fmt.Fprintf(&b, "\nThe prompts and answers are in `%s`.\n", p.Log)
	}
	return b.String()
}

// reason says in one line what kind of ending this was, since the lists below it are an agent's words
// rather than loomlc's.
func reason(p Problem) string {
	switch {
	case p.Outcome == 0:
		return "The run couldn't start."
	case len(p.Refusals) > 0:
		return "The work passed review, but loomlc wouldn't publish it."
	case p.Outcome == lifecycle.Blocked:
		return "A step stopped: it couldn't do this as the task describes it."
	case p.Outcome == lifecycle.Exhausted:
		return fmt.Sprintf("The work didn't pass review in %s.", passes(p.Passes))
	default:
		return "The run ended without proposing anything."
	}
}

func passes(n int) string {
	if n == 1 {
		return "1 pass"
	}
	return fmt.Sprintf("%d passes", n)
}

// writeItems writes a titled list. An item's later lines are indented, because gate output arrives as
// several lines and belongs to the item above it.
func writeItems(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\n**%s**\n\n", title)
	for _, item := range items {
		lines := strings.Split(strings.TrimRight(item, "\n"), "\n")
		fmt.Fprintf(b, "- %s\n", lines[0])
		for _, line := range lines[1:] {
			fmt.Fprintf(b, "  %s\n", line)
		}
	}
}
