package run

import (
	"fmt"
	"strings"

	"github.com/stritech-oss/loomlc/internal/lifecycle"
)

// Summary is the comment loomlc posts on a pull request to say how the run went: which model did what,
// how the work was sliced, and how the loop played out. It carries no session ids — a reviewer can't use
// one, and it would be a link to a transcript holding the task's untrusted text.
func Summary(result lifecycle.Result, steps []Step) string {
	var b strings.Builder
	b.WriteString("## How this was built\n\n")

	b.WriteString("| Step | Provider | Model |\n|---|---|---|\n")
	for _, s := range steps {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", s.Name, s.Provider, model(s))
	}

	// The agent's own words go on their own line, where the policy's line-anchored rules can see them.
	if summary := strings.TrimSpace(result.Plan.Summary); summary != "" {
		fmt.Fprintf(&b, "\n**The slice.**\n\n%s\n", summary)
	}
	if len(result.Plan.Deferred) > 0 {
		b.WriteString("\n**Left for another task.**\n")
		for _, item := range result.Plan.Deferred {
			fmt.Fprintf(&b, "- %s\n", item)
		}
	}

	fmt.Fprintf(&b, "\n**The loop.** %s\n", loop(result))
	if len(result.Gates) > 0 {
		b.WriteString("\n**Checks.**\n")
		for _, g := range result.Gates {
			fmt.Fprintf(&b, "- `%s` — %s\n", g.Name, passed(g.Passed))
		}
	}
	return b.String()
}

// Step is one step of the run, for saying what ran it.
type Step struct {
	Name, Provider, Model, Vendor string
}

func model(s Step) string {
	switch {
	case s.Model == "":
		return "the provider's default"
	case s.Vendor != "":
		return s.Vendor + "/" + s.Model
	default:
		return s.Model
	}
}

// loop says how many passes the work took and how it ended.
func loop(result lifecycle.Result) string {
	passes := "pass"
	if result.Passes != 1 {
		passes = "passes"
	}
	switch result.Outcome {
	case lifecycle.Proposed:
		return fmt.Sprintf("%d %s, then the review passed.", result.Passes, passes)
	case lifecycle.Exhausted:
		return fmt.Sprintf("%d %s without passing review.", result.Passes, passes)
	case lifecycle.Blocked:
		return "stopped before proposing anything."
	default:
		return fmt.Sprintf("%d %s.", result.Passes, passes)
	}
}

func passed(ok bool) string {
	if ok {
		return "passed"
	}
	return "**failed**"
}
