package lifecycle

import (
	"fmt"
	"strings"

	"github.com/stritech-oss/loomlc/internal/commit"
	"github.com/stritech-oss/loomlc/internal/gate"
	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/prompt"
)

// proposed converts a change step's commits into what the checker reads.
func proposed(change prompt.Change) []commit.Proposed {
	out := make([]commit.Proposed, 0, len(change.Commits))
	for _, c := range change.Commits {
		out = append(out, commit.Proposed{Message: c.Message, Paths: c.Paths, Fixes: c.Fixes})
	}
	return out
}

// branch converts the branch's commits into what the checker reads.
func branch(commits []git.Commit) []commit.Existing {
	out := make([]commit.Existing, 0, len(commits))
	for _, c := range commits {
		out = append(out, commit.Existing{SHA: c.SHA, Subject: c.Subject})
	}
	return out
}

// checkFindings carries the checker's problems into the next prompt, naming the commit each is about.
func checkFindings(problems []commit.Finding) []prompt.Finding {
	out := make([]prompt.Finding, 0, len(problems)+1)
	out = append(out, prompt.Finding{
		Detail: "loomlc made none of the commits you described, so the branch is unchanged. Describe them again, answering each point below.",
	})
	for _, p := range problems {
		detail := p.Detail
		if p.Commit > 0 {
			detail = fmt.Sprintf("commit %d %s", p.Commit, detail)
		}
		out = append(out, prompt.Finding{Path: p.Path, Detail: detail})
	}
	return out
}

// gateFindings turns failed checks into findings, so a failure comes back as work rather than a verdict.
func gateFindings(failed []gate.Result) []prompt.Finding {
	out := make([]prompt.Finding, 0, len(failed))
	for _, f := range failed {
		out = append(out, prompt.Finding{Detail: fmt.Sprintf("the check `%s` failed:\n%s", f.Name, strings.TrimSpace(f.Output))})
	}
	return out
}

// gateBlockers says which checks were already failing, for a run that can't start.
func gateBlockers(failed []gate.Result) []string {
	out := make([]string, 0, len(failed))
	for _, f := range failed {
		out = append(out, fmt.Sprintf("the check `%s` fails before this task's work starts:\n%s", f.Name, strings.TrimSpace(f.Output)))
	}
	return out
}

// verdictFindings converts a review step's findings for the next change prompt.
func verdictFindings(verdict prompt.Verdict) []prompt.Finding {
	out := make([]prompt.Finding, 0, len(verdict.Findings))
	for _, f := range verdict.Findings {
		out = append(out, prompt.Finding{Path: f.Path, Line: f.Line, Detail: f.Detail})
	}
	return out
}

func promptCommits(commits []git.Commit) []prompt.Commit {
	out := make([]prompt.Commit, 0, len(commits))
	for _, c := range commits {
		out = append(out, prompt.Commit{SHA: c.SHA, Subject: c.Subject})
	}
	return out
}

func promptGates(results []gate.Result) []prompt.Gate {
	out := make([]prompt.Gate, 0, len(results))
	for _, g := range results {
		out = append(out, prompt.Gate{Name: g.Name, Passed: g.Passed, Output: g.Output})
	}
	return out
}

// commands renders the argument vectors an agent may run, as it would type them.
func commands(allowed [][]string) []string {
	out := make([]string, 0, len(allowed))
	for _, argv := range allowed {
		out = append(out, strings.Join(argv, " "))
	}
	return out
}

// names lists failed checks for a progress line.
func names(failed []gate.Result) string {
	out := make([]string, 0, len(failed))
	for _, f := range failed {
		out = append(out, f.Name)
	}
	return strings.Join(out, ", ")
}

// renderPlan turns the plan into the text the steps after it read. The tests and deferred work are part
// of it: without them the change step never learns what has to pass, and the review step can't tell work
// that was deferred from work that was dropped.
func renderPlan(plan prompt.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nPull request title: %s\n", strings.TrimSpace(plan.Summary), plan.PRTitle)
	if len(plan.Files) > 0 {
		b.WriteString("\nFiles:\n")
		for _, f := range plan.Files {
			fmt.Fprintf(&b, "- %s — %s\n", f.Path, f.Change)
		}
	}
	writeList(&b, "Tests that must pass", plan.Tests)
	writeList(&b, "Deferred to another task", plan.Deferred)
	return b.String()
}

func writeList(b *strings.Builder, heading string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:\n", heading)
	for _, item := range items {
		fmt.Fprintf(b, "- %s\n", item)
	}
}

// exhaustedBlockers says what was still outstanding when the passes ran out.
func exhaustedBlockers(result Result) []string {
	switch {
	case len(result.Verdict.Findings) > 0:
		out := make([]string, 0, len(result.Verdict.Findings))
		for _, f := range result.Verdict.Findings {
			out = append(out, f.Detail)
		}
		return out
	case len(gate.Failed(result.Gates)) > 0:
		return []string{"the checks were still failing: " + names(gate.Failed(result.Gates))}
	default:
		return []string{fmt.Sprintf("the work didn't pass review in %d passes", result.Passes)}
	}
}
