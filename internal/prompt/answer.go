package prompt

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Plan is a planning step's answer, matching schema/plan.json.
type Plan struct {
	Status   string     `json:"status"`
	PRTitle  string     `json:"pr_title"`
	Summary  string     `json:"summary"`
	Files    []PlanFile `json:"files"`
	Tests    []string   `json:"tests"`
	Deferred []string   `json:"deferred"`
	Blockers []string   `json:"blockers"`
}

// PlanFile is one file the plan says to change.
type PlanFile struct {
	Path   string `json:"path"`
	Change string `json:"change"`
}

// Blocked reports whether the planner said the work can't proceed.
func (p Plan) Blocked() bool { return p.Status == "blocked" }

// Change is a change step's answer, matching schema/change.json.
type Change struct {
	Status    string         `json:"status"`
	Summary   string         `json:"summary"`
	Commits   []ChangeCommit `json:"commits"`
	Addressed []Addressed    `json:"addressed"`
	Blockers  []string       `json:"blockers"`
}

// ChangeCommit is one commit the change step asked loomlc to make.
type ChangeCommit struct {
	Message string   `json:"message"`
	Paths   []string `json:"paths"`
	Fixes   string   `json:"fixes"`
}

// Addressed is what the step did about one finding or comment.
type Addressed struct {
	Item     string `json:"item"`
	Response string `json:"response"`
}

// Blocked reports whether the step said it can't continue.
func (c Change) Blocked() bool { return c.Status == "blocked" }

// Complete reports whether the step said the work is ready to review.
func (c Change) Complete() bool { return c.Status == "complete" }

// Verdict is a review step's answer, matching schema/verdict.json.
type Verdict struct {
	Verdict  string           `json:"verdict"`
	Summary  string           `json:"summary"`
	Findings []VerdictFinding `json:"findings"`
}

// VerdictFinding is one defect the review step reported.
type VerdictFinding struct {
	Detail string `json:"detail"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
}

// Passed reports whether the change is ready for a human reviewer.
func (v Verdict) Passed() bool { return v.Verdict == "pass" }

// Answer decodes a step's answer into out and checks the fields loomlc acts on. The schema is enforced by
// the provider, but a provider that can't enforce one falls back to a fenced block, so this checks again.
func Answer(output string, raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return fmt.Errorf("the %s step returned no answer", output)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("the %s step's answer isn't the shape loomlc asked for: %w", output, err)
	}
	switch answer := out.(type) {
	case *Plan:
		return oneOf(output, "status", answer.Status, "ready", "blocked")
	case *Change:
		return oneOf(output, "status", answer.Status, "complete", "needs_more", "blocked")
	case *Verdict:
		return oneOf(output, "verdict", answer.Verdict, "pass", "fail")
	default:
		return fmt.Errorf("prompt: no answer type for a %s step", output)
	}
}

func oneOf(output, field, value string, allowed ...string) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("the %s step's %s is %q, which isn't one of %v", output, field, value, allowed)
}
